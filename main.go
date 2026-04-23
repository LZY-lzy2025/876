package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	// 统一入口，通过 ?format=txt 区分格式
	http.HandleFunc("/", handleUnifiedList)

	log.Printf("876 同步抓取服务已启动，监听端口: %s\n", port)
	log.Printf("访问地址示例: http://localhost:%s/ 或 http://localhost:%s/?format=txt\n", port, port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// 核心：JSONP 转 JSON 提取器
func extractJSON(body []byte) ([]byte, error) {
	s := string(body)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || start >= end {
		return nil, fmt.Errorf("invalid json")
	}
	return []byte(s[start : end+1]), nil
}

// 房间基础信息结构
type RoomBase struct {
	RoomNum string
	Title   string
	Anchor  string
}

// 抓取到的最终流地址信息
type StreamInfo struct {
	Name   string
	M3u8   string
	Flv    string
}

func handleUnifiedList(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")

	// 1. 获取所有房间列表
	allRoomsUrl := "https://json.yyzb456.top/all_live_rooms.json"
	req, _ := http.NewRequest("GET", allRoomsUrl, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "无法获取房间列表", 500)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	jsonBytes, err := extractJSON(bodyBytes)
	if err != nil {
		http.Error(w, "列表解析失败", 500)
		return
	}

	var allResult map[string]interface{}
	json.Unmarshal(jsonBytes, &allResult)
	data, _ := allResult["data"].(map[string]interface{})

	// 2. 提取并去重 RoomNum
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

	// 3. 并发抓取每个房间的详情（获取真实流地址）
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]StreamInfo, 0)
	
	// 限制并发数，防止被封 IP 或超时
	limit := make(chan struct{}, 10) 

	for _, room := range roomMap {
		wg.Add(1)
		go func(rb RoomBase) {
			defer wg.Done()
			limit <- struct{}{}        // 入队
			defer func() { <-limit }() // 出队

			detailUrl := fmt.Sprintf("https://json.yyzb456.top/room/%s/detail.json", rb.RoomNum)
			dReq, _ := http.NewRequest("GET", detailUrl, nil)
			dReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
			
			dClient := &http.Client{Timeout: 5 * time.Second}
			dResp, err := dClient.Do(dReq)
			if err != nil { return }
			defer dResp.Body.Close()

			dBody, _ := io.ReadAll(dResp.Body)
			dJson, err := extractJSON(dBody)
			if err != nil { return }

			var dRes map[string]interface{}
			json.Unmarshal(dJson, &dRes)
			
			dData, _ := dRes["data"].(map[string]interface{})
			stream, _ := dData["stream"].(map[string]interface{})
			
			hdM3u8, _ := stream["hdM3u8"].(string)
			hdFlv, _ := stream["hdFlv"].(string)

			if hdM3u8 != "" || hdFlv != "" {
				mu.Lock()
				results = append(results, StreamInfo{
					Name: fmt.Sprintf("%s-%s", rb.Anchor, rb.Title),
					M3u8: hdM3u8,
					Flv:  hdFlv,
				})
				mu.Unlock()
			}
		}(room)
	}
	wg.Wait()

	// 4. 根据格式输出结果
	if format == "txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, res := range results {
			if res.M3u8 != "" {
				fmt.Fprintf(w, "%s #M3U8,%s\n", res.Name, res.M3u8)
			}
			if res.Flv != "" {
				fmt.Fprintf(w, "%s #高清FLV,%s\n", res.Name, res.Flv)
			}
		}
	} else {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
		fmt.Fprintln(w, "#EXTM3U")
		for _, res := range results {
			if res.M3u8 != "" {
				fmt.Fprintf(w, "#EXTINF:-1 tvg-name=\"%s\",%s [M3U8]\n%s\n", res.Name, res.Name, res.M3u8)
			}
			if res.Flv != "" {
				fmt.Fprintf(w, "#EXTINF:-1 tvg-name=\"%s\",%s [高清FLV]\n%s\n", res.Name, res.Name, res.Flv)
			}
		}
	}
}
