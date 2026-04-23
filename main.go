package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	allRoomsRegex = regexp.MustCompile(`All_live_rooms\((.*)\)`)
	detailRegex   = regexp.MustCompile(`Detail\((.*)\)`)
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000" // 默认端口已修改为 10000
	}

	http.HandleFunc("/", handlePlaylist)         // 生成完整播放列表
	http.HandleFunc("/play/", handlePlay)        // 动态解析并重定向真实播放源

	log.Printf("服务已启动，监听端口: %s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// 路由1：输出全站 M3U 或 TXT 列表
func handlePlaylist(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "m3u"
	}

	// 纯净路径，无时间戳
	url := "https://json.yyzb456.top/all_live_rooms.json"

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "抓取房间列表失败", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	match := allRoomsRegex.FindSubmatch(bodyBytes)
	if len(match) < 2 {
		http.Error(w, "JSONP 解析失败", http.StatusInternalServerError)
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(match[1], &result); err != nil {
		http.Error(w, "JSON 转换失败", http.StatusInternalServerError)
		return
	}

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		http.Error(w, "数据结构异常", http.StatusInternalServerError)
		return
	}

	type Room struct {
		RoomNum string
		Title   string
		Anchor  string
	}
	roomMap := make(map[string]Room)

	for _, listRaw := range data {
		list, ok := listRaw.([]interface{})
		if !ok {
			continue
		}
		for _, itemRaw := range list {
			item, ok := itemRaw.(map[string]interface{})
			if !ok {
				continue
			}
			roomNum, _ := item["roomNum"].(string)
			title, _ := item["title"].(string)
			anchor := "未知主播"
			if anchorMap, ok := item["anchor"].(map[string]interface{}); ok {
				if nn, ok := anchorMap["nickName"].(string); ok {
					anchor = nn
				}
			}
			if roomNum != "" {
				roomMap[roomNum] = Room{RoomNum: roomNum, Title: title, Anchor: anchor}
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
		w.Header().Set("Cache-Control", "no-store")
		for _, room := range roomMap {
			fmt.Fprintf(w, "%s - %s,%s/play/%s.m3u8\n", room.Anchor, room.Title, baseUrl, room.RoomNum)
		}
	} else {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
		w.Header().Set("Content-Disposition", `inline; filename="playlist.m3u"`)
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintln(w, "#EXTM3U")
		for _, room := range roomMap {
			channelName := fmt.Sprintf("%s - %s", room.Anchor, room.Title)
			fmt.Fprintf(w, "#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\",%s\n%s/play/%s.m3u8\n", room.RoomNum, channelName, channelName, baseUrl, room.RoomNum)
		}
	}
}

// 路由2：根据房间号动态抓取真实 M3U8 并重定向
func handlePlay(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/play/")
	roomNum := strings.TrimSuffix(path, ".m3u8")
	if roomNum == "" {
		http.Error(w, "房间号无效", http.StatusBadRequest)
		return
	}

	// 纯净路径，无时间戳
	url := fmt.Sprintf("https://json.yyzb456.top/room/%s/detail.json", roomNum)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "请求详情接口失败", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	match := detailRegex.FindSubmatch(bodyBytes)
	if len(match) < 2 {
		http.Error(w, "房间未找到或已下播", http.StatusNotFound)
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(match[1], &result); err != nil {
		http.Error(w, "详情 JSON 解析错误", http.StatusInternalServerError)
		return
	}

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		http.Error(w, "数据结构异常", http.StatusInternalServerError)
		return
	}
	
	stream, ok := data["stream"].(map[string]interface{})
	if !ok {
		http.Error(w, "未找到播放流信息", http.StatusNotFound)
		return
	}

	hdM3u8, ok := stream["hdM3u8"].(string)
	if !ok || hdM3u8 == "" {
		http.Error(w, "高清源暂不可用", http.StatusNotFound)
		return
	}

	http.Redirect(w, r, hdM3u8, http.StatusFound)
}
