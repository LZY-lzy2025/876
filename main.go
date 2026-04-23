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

	log.Printf("876 服务启动 | 台标增强版 | 端口: %s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// 通用 JSONP/JSON 提取
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
	Logo       string // 台标 URL
	CreateTime int64  // 用于排序
}

func handleUnifiedList(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")

	// 1. 获取全站房间列表
	allRoomsUrl := "https://json.yyzb456.top/all_live_rooms.json"
	req, _ := http.NewRequest("GET", allRoomsUrl, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Fetch List Error", 500)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	jsonBytes, err := extractJSON(bodyBytes)
	if err != nil {
		http.Error(w, "Parse List Error", 500)
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

	// 2. 并发抓取详情（台标+地址+时间）
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]StreamInfo, 0)
	limit := make(chan struct{}, 20) // 适当增加并发

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
			roomObj, _ := dData["room"].(map[string]interface{})
			anchorObj, _ := roomObj["anchor"].(map[string]interface{})
			
			// 抓取台标 (icon) 和 创建时间
			logoUrl, _ := anchorObj["icon"].(string)
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
					Logo:       logoUrl,
					CreateTime: int64(cTime),
				})
				mu.Unlock()
			}
		}(room)
	}
	wg.Wait()

	// 3. 按时间降序排序（最新在最前）
	sort.Slice(results, func(i, j int) bool {
		return results[i].CreateTime > results[j].CreateTime
	})

	// 4. 分组
	var sateRooms, anchorRooms []StreamInfo
	for _, res := range results {
		if strings.Contains(res.Name, "卫星") {
			sateRooms = append(sateRooms, res)
		} else {
			anchorRooms = append(anchorRooms, res)
		}
	}

	// 5. 输出格式化
	if format == "txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "卫星线路1,#genre#")
		for _, res := range sateRooms {
			writeTxtLine(w, res)
		}
		fmt.Fprintln(w, "主播线路1,#genre#")
		for _, res := range anchorRooms {
			writeTxtLine(w, res)
		}
	} else {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
		fmt.Fprintln(w, "#EXTM3U")
		for _, res := range sateRooms {
			writeM3uLine(w, res, "卫星线路1")
		}
		for _, res := range anchorRooms {
			writeM3uLine(w, res, "主播线路1")
		}
	}
}

func writeTxtLine(w http.ResponseWriter, res StreamInfo) {
	// TXT 格式：频道名$台标URL,地址
	if res.M3u8 != "" {
		fmt.Fprintf(w, "%s [HLS]$%s,%s\n", res.Name, res.Logo, res.M3u8)
	}
	if res.Flv != "" {
		fmt.Fprintf(w, "%s [高清FLV]$%s,%s\n", res.Name, res.Logo, res.Flv)
	}
}

func writeM3uLine(w http.ResponseWriter, res StreamInfo, group string) {
	// M3U 格式：使用 tvg-logo 标签
	if res.M3u8 != "" {
		fmt.Fprintf(w, "#EXTINF:-1 group-title=\"%s\" tvg-logo=\"%s\" tvg-name=\"%s\",%s [HLS]\n%s\n", group, res.Logo, res.Name, res.Name, res.M3u8)
	}
	if res.Flv != "" {
		fmt.Fprintf(w, "#EXTINF:-1 group-title=\"%s\" tvg-logo=\"%s\" tvg-name=\"%s\",%s [高清FLV]\n%s\n", group, res.Logo, res.Name, res.Name, res.Flv)
	}
}
