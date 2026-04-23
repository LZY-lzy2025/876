package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	http.HandleFunc("/", handleUnifiedList)

	log.Printf("876 服务已启动 | 排序模式 | 分组模式 | 端口: %s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func extractJSON(body []byte) ([]byte, error) {
	s := string(body)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || start >= end {
		return nil, fmt.Errorf("invalid json")
	}
	return []byte(s[start : end+1]), nil
}

type RoomBase struct {
	RoomNum string
	Title   string
	Anchor  string
}

type StreamInfo struct {
	Name       string
	M3u8       string
	Flv        string
	CreateTime int64 // 用于排序
}

func handleUnifiedList(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")

	// 1. 获取全站房间
	allRoomsUrl := "https://json.yyzb456.top/all_live_rooms.json"
	req, _ := http.NewRequest("GET", allRoomsUrl, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Fetch Error", 500)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	jsonBytes, err := extractJSON(bodyBytes)
	if err != nil {
		http.Error(w, "Parse Error", 500)
		return
	}

	var allResult map[string]interface{}
	json.Unmarshal(jsonBytes, &allResult)
	data, _ := allResult["data"].(map[string]interface{})

	roomMap := make(map[string]RoomBase)
	for _, val := range data {
		if list, ok := val.([]interface{}); ok {
			for _, itemRaw := range list {
				if item, ok := itemRaw.(map[string]interface{}); ok {
					rNum, _ := item["roomNum"].(string)
					title, _ := item["title"].(string)
					anchor := "未知主播"
					if aObj, ok := item["anchor"].(map[string]interface{}); ok {
						anchor, _ = aObj["nickName"].(string)
					}
					if rNum != "" {
						roomMap[rNum] = RoomBase{rNum, title, anchor}
					}
				}
			}
		}
	}

	// 2. 并发解析详情
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]StreamInfo, 0)
	limit := make(chan struct{}, 15) // 并发度

	for _, room := range roomMap {
		wg.Add(1)
		go func(rb RoomBase) {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()

			dUrl := fmt.Sprintf("https://json.yyzb456.top/room/%s/detail.json", rb.RoomNum)
			dResp, err := http.Get(dUrl)
			if err != nil { return }
			defer dResp.Body.Close()

			dB, _ := io.ReadAll(dResp.Body)
			dJ, err := extractJSON(dB)
			if err != nil { return }

			var dRes map[string]interface{}
			json.Unmarshal(dJ, &dRes)
			
			dData, _ := dRes["data"].(map[string]interface{})
			// 提取创建时间用于排序
			roomObj, _ := dData["room"].(map[string]interface{})
			anchorObj, _ := roomObj["anchor"].(map[string]interface{})
			cTime, _ := anchorObj["createTime"].(float64)

			stream, _ := dData["stream"].(map[string]interface{})
			hdM3u8, _ := stream["hdM3u8"].(string)
			hdFlv, _ := stream["hdFlv"].(string)

			if hdM3u8 != "" || hdFlv != "" {
				mu.Lock()
				results = append(results, StreamInfo{
					Name:       fmt.Sprintf("%s-%s", rb.Anchor, rb.Title),
					M3u8:       hdM3u8,
					Flv:        hdFlv,
					CreateTime: int64(cTime),
				})
				mu.Unlock()
			}
		}(room)
	}
	wg.Wait()

	// 3. 排序逻辑：距现在时间越近（CreateTime 越大）排名越靠前
	sort.Slice(results, func(i, j int) bool {
		return results[i].CreateTime > results[j].CreateTime
	})

	// 4. 分组逻辑
	var sateRooms, anchorRooms []StreamInfo
	for _, res := range results {
		if strings.Contains(res.Name, "卫星") {
			sateRooms = append(sateRooms, res)
		} else {
			anchorRooms = append(anchorRooms, res)
		}
	}

	// 5. 输出
	if format == "txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		// 输出卫星组
		fmt.Fprintln(w, "卫星线路1,#genre#")
		for _, res := range sateRooms {
			writeTxt(w, res)
		}
		// 输出主播组
		fmt.Fprintln(w, "主播线路1,#genre#")
		for _, res := range anchorRooms {
			writeTxt(w, res)
		}
	} else {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
		fmt.Fprintln(w, "#EXTM3U")
		// 输出卫星组 (M3U 格式通常用 group-title 分组)
		for _, res := range sateRooms {
			writeM3u(w, res, "卫星线路1")
		}
		// 输出主播组
		for _, res := range anchorRooms {
			writeM3u(w, res, "主播线路1")
		}
	}
}

func writeTxt(w http.ResponseWriter, res StreamInfo) {
	if res.M3u8 != "" {
		fmt.Fprintf(w, "%s #M3U8,%s\n", res.Name, res.M3u8)
	}
	if res.Flv != "" {
		fmt.Fprintf(w, "%s #高清FLV,%s\n", res.Name, res.Flv)
	}
}

func writeM3u(w http.ResponseWriter, res StreamInfo, group string) {
	if res.M3u8 != "" {
		fmt.Fprintf(w, "#EXTINF:-1 group-title=\"%s\" tvg-name=\"%s\",%s [M3U8]\n%s\n", group, res.Name, res.Name, res.M3u8)
	}
	if res.Flv != "" {
		fmt.Fprintf(w, "#EXTINF:-1 group-title=\"%s\" tvg-name=\"%s\",%s [高清FLV]\n%s\n", group, res.Name, res.Name, res.Flv)
	}
}
