package organize

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"smartstrm/internal/driver"
)

// mockFS 模拟 OpenList 文件树：dir -> name -> isDir
type mockFS struct {
	mu   sync.Mutex
	root map[string]map[string]bool
}

func newMockFS() *mockFS {
	return &mockFS{root: map[string]map[string]bool{"/": {}}}
}

func (m *mockFS) ensureDirEntry(dir string) {
	if _, ok := m.root[dir]; !ok {
		m.root[dir] = map[string]bool{}
	}
}

func (m *mockFS) List(_ context.Context, path string) ([]driver.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir := strings.TrimSuffix(path, "/")
	names, ok := m.root[dir]
	if !ok {
		return []driver.File{}, errNotFound
	}
	var out []driver.File
	for name, isDir := range names {
		out = append(out, driver.File{Name: name, IsDir: isDir})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *mockFS) Mkdir(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, name := driver.SplitPath(path)
	m.ensureDirEntry(dir)
	if m.root[dir][name] {
		return errExists
	}
	m.root[dir][name] = true
	m.ensureDirEntry(path)
	return nil
}

func (m *mockFS) Rename(_ context.Context, path, newName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, name := driver.SplitPath(path)
	isDir := m.root[dir][name]
	delete(m.root[dir], name)
	m.root[dir][newName] = isDir
	return nil
}

func (m *mockFS) Move(_ context.Context, srcDir, dstDir string, names []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDirEntry(srcDir)
	m.ensureDirEntry(dstDir)
	for _, n := range names {
		isDir := m.root[srcDir][n]
		if m.root[dstDir][n] {
			return errExists
		}
		delete(m.root[srcDir], n)
		m.root[dstDir][n] = isDir
		if isDir {
			// 移动目录时同时迁移其子树
			m.moveTree(srcDir+"/"+n, dstDir+"/"+n)
		}
	}
	return nil
}

func (m *mockFS) moveTree(src, dst string) {
	if _, ok := m.root[src]; !ok {
		return
	}
	m.root[dst] = m.root[src]
	delete(m.root, src)
	for child, isDir := range m.root[dst] {
		if isDir {
			m.moveTree(src+"/"+child, dst+"/"+child)
		}
	}
}

func (m *mockFS) Remove(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, name := driver.SplitPath(path)
	delete(m.root[dir], name)
	delete(m.root, path)
	return nil
}

var (
	errNotFound = errorNotFound{}
	errExists   = errorExists{}
)

type errorNotFound struct{}

func (errorNotFound) Error() string { return "path not found" }

type errorExists struct{}

func (errorExists) Error() string { return "file exists" }

// seedCli 预置文件：/115/AV-cli 下散落的视频
func seedCli(t *testing.T, m *mockFS) {
	m.mu.Lock()
	m.mu.Unlock()
	dir := "/115/AV-cli"
	m.root[dir] = map[string]bool{
		"vrkm00919-CD9.mp4":      false,
		"hmn00035.mp4":           false,
		"abc123(uncensored).mp4": false,
	}
}

func has(m *mockFS, dir, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.root[dir][name]
	return ok
}

// TestOrganizeDryRun 预览不落地
func TestOrganizeDryRun(t *testing.T) {
	m := newMockFS()
	seedCli(t, m)
	client := m
	res, err := Run(context.Background(), client, Options{
		TargetPath: "/115/AV-cli", Mode: "all", IDMode: "AV",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 干运行应生成完整计划（整理 + 移动）
	if len(res.Plan) == 0 {
		t.Fatal("dry-run 应有计划")
	}
	// 且文件不应被移动
	if has(m, "/115/AV-cli", "hmn00035.mp4") == false {
		t.Fatal("dry-run 不应实际移动文件")
	}
}

// TestOrganizeAll 整理并移动到分类目录
func TestOrganizeAll(t *testing.T) {
	m := newMockFS()
	seedCli(t, m)
	res, err := Run(context.Background(), m, Options{
		TargetPath: "/115/AV-cli", Mode: "all", IDMode: "AV",
		DryRun: false, Overwrite: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// vrkm00919-CD9 整理进 /115/AV-cli/VRKM-919，再移动到 /115/AV/V/VRKM-919
	if !has(m, "/115/AV/V", "VRKM-919") {
		t.Fatal("VRKM-919 应在 /115/AV/V 下")
	}
	if !has(m, "/115/AV/V/VRKM-919", "VRKM-919-CD9.mp4") {
		t.Fatal("VRKM-919-CD9.mp4 应在分类目录内")
	}
	// HMN-035 是单部，整理进 /115/AV/H/HMN-035
	if !has(m, "/115/AV/H", "HMN-035") {
		t.Fatal("HMN-035 应在 /115/AV/H 下")
	}
	// abc123(uncensored) 单部且含 uncensored → 视为 -U 标签，进入 /115/AV/A/ABC-123-U
	if !has(m, "/115/AV/A", "ABC-123-U") {
		t.Fatal("ABC-123-U 应在 /115/AV/A 下")
	}
	if !has(m, "/115/AV/A/ABC-123-U", "ABC-123-U.mp4") {
		t.Fatal("ABC-123-U.mp4 应在目录内")
	}
	if len(res.Errors) != 0 {
		t.Fatalf("应无错误，got %v", res.Errors)
	}
}
