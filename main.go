package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	// 端口设定为 10000
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	http.HandleFunc("/", handlePlaylist)  // 生成 M3U/TXT 列表
	http.HandleFunc("/play/", handlePlay) // 动态解析房间 M3U8 并重定向

	log.Printf("876 服务已启动，监听端口: %s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// 核心函数：通用 JSONP 转 JSON 提取器
func extractJSON(body []byte) ([]byte, error) {
	s := string(body)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || start >= end {
		return nil, fmt.Errorf("could not find JSON object")
	}
	return []byte(s[start : end+1]), nil
}

func handlePlaylist(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	format := r.URL.Query().Get("format")
	
	// 纯净接口，不带时间戳
	url := "https://json.yyzb456.top/all_live_rooms.json"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	
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

	var result map[string]interface{}
	json.Unmarshal(jsonBytes, &result)

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		http.Error(w, "Data Format Error", 500)
		return
	}

	type Room struct {
		RoomNum string
		Title   string
		Anchor  string
	}
	roomMap := make(map[string]Room)

	// 核心遍历：提取 data 下所有分类中的房间
	for _, val := range data {
		if list, ok := val.([]interface{}); ok {
			for _, itemRaw := range list {
				if item, ok := itemRaw.(map[string]interface{}); ok {
					roomNum, _ := item["roomNum"].(string)
					title, _ := item["title"].(string)
					anchorName := "未知主播"
					if anchor, ok := item["anchor"].(map[string]interface{}); ok {
						anchorName, _ = anchor["nickName"].(string)
					}
					if roomNum != "" {
						roomMap[roomNum] = Room{roomNum, title, anchorName}
					}
				}
			}
		}
	}

	scheme := "http"
	if r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	baseUrl := fmt.Sprintf("%s://%s", scheme, r.Host)

	if format == "txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, room := range roomMap {
			fmt.Fprintf(w, "%s - %s,%s/play/%s.m3u8\n", room.Anchor, room.Title, baseUrl, room.RoomNum)
		}
	} else {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
		fmt.Fprintln(w, "#EXTM3U")
		for _, room := range roomMap {
			name := fmt.Sprintf("%s - %s", room.Anchor, room.Title)
			fmt.Fprintf(w, "#EXTINF:-1 tvg-name=\"%s\",%s\n%s/play/%s.m3u8\n", name, name, baseUrl, room.RoomNum)
		}
	}
}

func handlePlay(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/play/")
	roomNum := strings.TrimSuffix(path, ".m3u8")
	
	// 纯净详情接口
	url := fmt.Sprintf("https://json.yyzb456.top/room/%s/detail.json", roomNum)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Stream Fetch Failed", 500)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	jsonBytes, err := extractJSON(bodyBytes)
	if err != nil {
		http.Error(w, "Detail Parse Failed", 404)
		return
	}

	var result map[string]interface{}
	json.Unmarshal(jsonBytes, &result)

	// 提取 hdM3u8 地址
	resData, _ := result["data"].(map[string]interface{})
	stream, _ := resData["stream"].(map[string]interface{})
	hdM3u8, _ := stream["hdM3u8"].(string)

	if hdM3u8 == "" {
		http.Error(w, "No Stream URL", 404)
		return
	}

	// 302 重定向到真实的鉴权流地址
	http.Redirect(w, r, hdM3u8, http.StatusFound)
}
