package db

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDirCache 验证目录时间缓存的写入/读取与千条级性能
func TestDirCache(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// 模拟 3000 个目录
	cache := map[string]int64{}
	for i := 0; i < 3000; i++ {
		cache[fmt.Sprintf("/115/FC2/D%04d", i)] = int64(1700000000 + i)
	}

	start := time.Now()
	if err := db.SaveDirCache("FC2", cache); err != nil {
		t.Fatalf("save: %v", err)
	}
	t.Logf("写入 3000 条耗时: %v", time.Since(start))

	start = time.Now()
	got, err := db.LoadDirCache("FC2")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Logf("读取 3000 条耗时: %v", time.Since(start))

	if len(got) != len(cache) {
		t.Fatalf("数量不一致: got %d want %d", len(got), len(cache))
	}
	for k, v := range cache {
		if got[k] != v {
			t.Fatalf("值不一致: %s got %d want %d", k, got[k], v)
		}
	}

	// 任务隔离：另一个任务的缓存互不影响
	if err := db.SaveDirCache("AV", map[string]int64{"/115/AV": 42}); err != nil {
		t.Fatalf("save AV: %v", err)
	}
	av, _ := db.LoadDirCache("AV")
	if len(av) != 1 {
		t.Fatalf("AV 缓存应隔离，got %d", len(av))
	}
	fc2, _ := db.LoadDirCache("FC2")
	if len(fc2) != 3000 {
		t.Fatalf("FC2 缓存被污染，got %d", len(fc2))
	}

	// 更新已存在条目（upsert）
	cache["/115/FC2/D0000"] = 999
	if err := db.SaveDirCache("FC2", map[string]int64{"/115/FC2/D0000": 999}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got2, _ := db.LoadDirCache("FC2")
	if got2["/115/FC2/D0000"] != 999 {
		t.Fatalf("upsert 失败: got %d", got2["/115/FC2/D0000"])
	}
	if len(got2) != 3000 {
		t.Fatalf("upsert 后数量变化: got %d", len(got2))
	}

	// 文件体积对比：同量级 JSON 文件 vs DB
	jsonFile := filepath.Join(dir, "dir_cache.json")
	f, _ := os.Create(jsonFile)
	defer f.Close()
	fmt.Fprint(f, "{")
	first := true
	for k, v := range cache {
		if !first {
			fmt.Fprint(f, ",")
		}
		first = false
		fmt.Fprintf(f, "%q:%d", k, v)
	}
	fmt.Fprint(f, "}")
	f.Close()
	stat, _ := os.Stat(jsonFile)
	t.Logf("3000 条 JSON 文件体积: %d 字节", stat.Size())
	statDB, _ := os.Stat(filepath.Join(dir, "test.db"))
	t.Logf("SQLite 数据库体积: %d 字节", statDB.Size())
}
