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
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	http.HandleFunc("/", handlePlaylist)
	http.HandleFunc("/play/", handlePlay)

	log.Printf("876 服务启动成功！端口: %s (支持 M3U8 & 高清 FLV)\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// 核心：JSONP 智能提取器（截取第一个 { 和最后一个 }）
func extractJSON(body []byte) ([]byte, error) {
	s := string(body)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || start >= end {
		return nil, fmt.Errorf("未找到有效的 JSON 内容")
	}
	return []byte(s[start : end+1]), nil
}

func handlePlaylist(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	format := r.URL.Query().Get("format")
	url := "https://json.yyzb456.top/all_live_rooms.json"
	
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "抓取列表失败", 500)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	jsonBytes, err := extractJSON(bodyBytes)
	if err != nil {
		http.Error(w, "列表解析失败", 500)
		return
	}

	var result map[string]interface{}
	json.Unmarshal(jsonBytes, &result)

	data, _ := result["data"].(map[string]interface{})
	
	type Room struct {
		RoomNum string
		Title   string
		Anchor  string
	}
	roomMap := make(map[string]Room)

	// 遍历所有分类（0, 1, hot 等），提取所有房间并去重
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

	// --- 输出逻辑 ---
	if format == "txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, room := range roomMap {
			// 同时输出 M3U8 和 高清 FLV 线路到文本
			fmt.Fprintf(w, "%s - %s #M3U8,%s/play/%s.m3u8\n", room.Anchor, room.Title, baseUrl, room.RoomNum)
			fmt.Fprintf(w, "%s - %s #高清FLV,%s/play/%s.flv\n", room.Anchor, room.Title, baseUrl, room.RoomNum)
		}
	} else {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
		fmt.Fprintln(w, "#EXTM3U")
		for _, room := range roomMap {
			displayName := fmt.Sprintf("%s - %s", room.Anchor, room.Title)
			// 输出 M3U8 频道
			fmt.Fprintf(w, "#EXTINF:-1 tvg-name=\"%s\",%s [HLS]\n%s/play/%s.m3u8\n", displayName, displayName, baseUrl, room.RoomNum)
			// 输出高清 FLV 频道
			fmt.Fprintf(w, "#EXTINF:-1 tvg-name=\"%s\",%s [高清FLV]\n%s/play/%s.flv\n", displayName, displayName, baseUrl, room.RoomNum)
		}
	}
}

func handlePlay(w http.ResponseWriter, r *http.Request) {
	// 判断请求后缀是 .flv 还是 .m3u8
	isFlv := strings.HasSuffix(r.URL.Path, ".flv")
	path := strings.TrimPrefix(r.URL.Path, "/play/")
	roomNum := strings.TrimSuffix(strings.TrimSuffix(path, ".m3u8"), ".flv")

	// 请求该房号的详情接口
	detailUrl := fmt.Sprintf("https://json.yyzb456.top/room/%s/detail.json", roomNum)
	req, _ := http.NewRequest("GET", detailUrl, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "详情获取失败", 500)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	jsonBytes, err := extractJSON(bodyBytes)
	if err != nil {
		http.Error(w, "详情解析失败", 404)
		return
	}

	var result map[string]interface{}
	json.Unmarshal(jsonBytes, &result)

	// 提取 data -> stream 里的链接
	resData, _ := result["data"].(map[string]interface{})
	stream, _ := resData["stream"].(map[string]interface{})

	var finalUrl string
	if isFlv {
		// 重点：抓取高清 FLV 字段 hdFlv
		finalUrl, _ = stream["hdFlv"].(string)
	} else {
		// 抓取高清 M3U8 字段 hdM3u8
		finalUrl, _ = stream["hdM3u8"].(string)
	}

	if finalUrl == "" {
		http.Error(w, "直播源暂不可用", 404)
		return
	}

	// 302 重定向到真实的播放地址（带鉴权 Token）
	http.Redirect(w, r, finalUrl, http.StatusFound)
}
