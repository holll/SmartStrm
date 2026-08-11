// mock OpenList 服务器，用于冒烟测试
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// 测试目录结构
var tree = map[string][]entry{
	"/": {
		{Name: "115", IsDir: true, Modified: "2026-08-11 12:00:00"},
	},
	"/115": {
		{Name: "FC2", IsDir: true, Modified: "2026-08-11 12:00:00"},
		{Name: "AV", IsDir: true, Modified: "2026-08-11 12:00:00"},
	},
	"/115/FC2": {
		{Name: "A", IsDir: true, Modified: "2026-08-11 12:00:00"},
		{Name: "B", IsDir: true, Modified: "2026-08-10 12:00:00"},
		{Name: "sample.mp4", Size: 50 * 1024 * 1024, Modified: "2026-08-11 12:00:00"},
		{Name: "advert.mkv", Size: 300 * 1024 * 1024, Modified: "2026-08-11 12:00:00"},
	},
	"/115/FC2/A": {
		{Name: "ADN-468.mp4", Size: 300 * 1024 * 1024, Modified: "2026-08-11 12:00:00"},
		{Name: "ADN-468.nfo", Size: 2048, Modified: "2026-08-11 12:00:00"},
		{Name: "ADN-468.jpg", Size: 100 * 1024, Modified: "2026-08-11 12:00:00"},
		{Name: "tiny.mp4", Size: 1024, Modified: "2026-08-11 12:00:00"}, // 小于阈值应被跳过
	},
	"/115/FC2/B": {},
	"/115/AV": {
		{Name: "Movie-2026.mp4", Size: 1024 * 1024 * 1024, Modified: "2026-08-11 12:00:00"},
	},
}

type entry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	IsDir    bool   `json:"is_dir"`
	Modified string `json:"modified"`
}

// 大数据量目录：50 个子目录各含 1 个文件，用于测试停止与日志实时刷新
func init() {
	for i := 1; i <= 50; i++ {
		d := fmt.Sprintf("D%02d", i)
		tree["/115/FC2/"+d] = []entry{
			{Name: fmt.Sprintf("file_%02d.mp4", i), Size: 100 * 1024 * 1024, Modified: "2026-08-11 12:00:00"},
		}
		tree["/115/FC2"] = append(tree["/115/FC2"], entry{Name: d, IsDir: true, Modified: "2026-08-11 12:00:00"})
	}
}

type apiResp struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func handleList(w http.ResponseWriter, r *http.Request) {
	time.Sleep(80 * time.Millisecond) // 模拟真实网络延迟，便于测试停止/日志
	var req struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	content, ok := tree[req.Path]
	if !ok {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(apiResp{Code: 404, Message: "path not found"})
		return
	}
	json.NewEncoder(w).Encode(apiResp{Code: 200, Message: "success", Data: map[string]any{"content": content}})
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	json.NewEncoder(w).Encode(apiResp{Code: 200, Message: "success", Data: map[string]any{
		"raw_url": fmt.Sprintf("http://127.0.0.1:19090/d%s", req.Path),
		"url":     fmt.Sprintf("http://127.0.0.1:19090/d%s", req.Path),
	}})
}

func handleRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if _, ok := tree[req.Path]; ok {
		delete(tree, req.Path)
		json.NewEncoder(w).Encode(apiResp{Code: 200, Message: "success"})
		return
	}
	// 尝试按文件删除：遍历父目录
	for dir, entries := range tree {
		parent := strings.TrimSuffix(dir, "/") + "/"
		for i, e := range entries {
			if parent+e.Name == req.Path {
				tree[dir] = append(entries[:i], entries[i+1:]...)
				json.NewEncoder(w).Encode(apiResp{Code: 200, Message: "success"})
				return
			}
		}
	}
	json.NewEncoder(w).Encode(apiResp{Code: 404, Message: "not found"})
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/d")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	// 直接匹配文件：父目录下查找
	parent := path[:strings.LastIndex(path, "/")]
	name := path[strings.LastIndex(path, "/")+1:]
	if entries, ok := tree[parent]; ok {
		for _, e := range entries {
			if !e.IsDir && e.Name == name {
				w.Write([]byte("mock-content:" + path))
				return
			}
		}
	}
	http.NotFound(w, r)
}

func main() {
	http.HandleFunc("/api/fs/list", handleList)
	http.HandleFunc("/api/fs/get", handleGet)
	http.HandleFunc("/api/fs/remove", handleRemove)
	http.HandleFunc("/d/", handleDownload)
	log.Printf("mock openlist listening on :19090")
	log.Fatal(http.ListenAndServe(":19090", nil))
	_ = time.Now
}
