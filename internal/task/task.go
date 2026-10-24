// Package task 任务管理与 Cron 调度
package task

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"smartstrm/internal/config"
	"smartstrm/internal/db"
	"smartstrm/internal/driver"
	"smartstrm/internal/generator"
	"smartstrm/internal/plugins"
)

// chinaTZ 中国时区（UTC+8，无夏令时）。cron 表达式按此时区解释：
// 避免服务器时区为 UTC 时，`44 4 * * *` 被理解成 UTC 04:44（= 北京时间 12:44）执行
var chinaTZ = time.FixedZone("CST", 8*3600)

// LogLine 任务日志行
type LogLine struct {
	Seq   int64     `json:"seq"`
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

// logBufferMax 实时日志缓冲上限（环形）。生成器每目录一行 INFO，
// 上限过小会挤出早期日志，导致落库的历史日志缺前半段
const logBufferMax = 50000

// LogBuffer 任务日志缓冲（环形，按任务隔离，支持 SSE 订阅者）
type LogBuffer struct {
	mu            sync.Mutex
	lines         []LogLine
	max           int
	seq           int64
	archive       []LogLine
	persist       func([]LogLine) error
	persistFailed bool
	retain        bool
	frozen        bool

	subs  map[int64]chan LogLine // SSE 订阅者：id → 增量行通道
	subID int64
}

func persistRunLogs(database *db.DB, runID int64, lines []LogLine) error {
	stored := make([]struct {
		Time  time.Time
		Level string
		Msg   string
	}, len(lines))
	for i, l := range lines {
		stored[i] = struct {
			Time  time.Time
			Level string
			Msg   string
		}{l.Time, l.Level, l.Msg}
	}
	return database.InsertLogs(runID, stored)
}

// NewLogBuffer 创建日志缓冲
func NewLogBuffer(max int) *LogBuffer {
	return &LogBuffer{max: max, subs: map[int64]chan LogLine{}}
}

// Logf 追加日志并广播给订阅者
func (b *LogBuffer) Logf(level, format string, args ...any) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen {
		return
	}
	b.seq++
	line := LogLine{Seq: b.seq, Time: time.Now(), Level: level, Msg: fmt.Sprintf(format, args...)}
	b.lines = append(b.lines, line)
	if b.retain {
		b.archive = append(b.archive, line)
		if len(b.archive) >= 1000 && b.persist != nil && !b.persistFailed {
			if err := b.persist(b.archive); err != nil {
				b.persistFailed = true
			} else {
				b.archive = nil
			}
		}
	}
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
	for id, ch := range b.subs {
		// Overflow closes the subscription: SSE sends an explicit resync notice instead of silently dropping lines.
		select {
		case ch <- line:
		default:
			close(ch)
			delete(b.subs, id)
		}
	}
}

// Subscribe 注册 SSE 订阅者，返回：
//   - 增量行通道（从订阅时刻起推送）
//   - 订阅时刻的 seq 快照（调用方先写 [from, snapSeq] 历史，再读通道，保证无丢失无乱序）
//   - 注销函数（连接断开时调用）
func (b *LogBuffer) Subscribe() (<-chan LogLine, int64, func()) {
	if b == nil {
		ch := make(chan LogLine)
		return ch, 0, func() {}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan LogLine, 1024)
	if b.frozen {
		close(ch)
		return ch, b.seq, func() {}
	}
	b.subID++
	id := b.subID
	b.subs[id] = ch
	return ch, b.seq, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

// History 返回 (from, to] 范围内的日志（SSE 连接的初始同步）
func (b *LogBuffer) History(from, to int64) []LogLine {
	if b == nil || to <= from {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]LogLine, 0, 64)
	for _, l := range b.lines {
		if l.Seq > from && l.Seq <= to {
			out = append(out, l)
		}
	}
	return out
}

// Since 返回 seq 之后的增量日志
// 若缓冲曾被 Reset（seq 重新从 0 计数）且请求的 seq 比当前最大还大，
// 说明客户端持有的是上一轮的 seq，此时返回全部（模拟从新运行开始）
func (b *LogBuffer) Since(seq int64) []LogLine {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) == 0 {
		return nil
	}
	if seq > b.seq {
		return append([]LogLine(nil), b.lines...)
	}
	start := 0
	for start < len(b.lines) && b.lines[start].Seq <= seq {
		start++
	}
	return append([]LogLine(nil), b.lines[start:]...)
}

// LastSeq 当前最大 seq
func (b *LogBuffer) LastSeq() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// Reset 清空日志
func (b *LogBuffer) Reset() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.lines = nil
	b.seq = 0
	b.mu.Unlock()
}

// Manager 任务管理器
type Manager struct {
	cfg        *config.Config
	db         *db.DB
	cron       *cron.Cron
	entryID    map[string]cron.EntryID // 任务名 → Cron 条目
	scheduleMu sync.Mutex
	mu         sync.Mutex
	activeDirs map[string]string             // Snapshot output directory retained across configuration edits
	running    map[string]bool               // 正在运行的任务
	cancel     map[string]context.CancelFunc // 运行中任务的取消函数
	results    map[string]*Result            // 最近一次运行结果
	logs       map[string]*LogBuffer         // 任务日志缓冲
	onState    func()                        // 状态变化回调（管理页轮询用）
}

// Result 任务运行状态
type Result struct {
	Running bool              `json:"running"`
	Start   time.Time         `json:"start"`
	End     time.Time         `json:"end"`
	Result  *generator.Result `json:"result"`
	Error   string            `json:"error,omitempty"`
}

// New 创建任务管理器
func New(cfg *config.Config, database *db.DB) *Manager {
	return &Manager{
		cfg:        cfg,
		db:         database,
		cron:       cron.New(cron.WithLocation(chinaTZ), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger))),
		entryID:    map[string]cron.EntryID{},
		activeDirs: map[string]string{},
		running:    map[string]bool{},
		cancel:     map[string]context.CancelFunc{},
		results:    map[string]*Result{},
		logs:       map[string]*LogBuffer{},
	}
}

// DB 返回数据库实例
func (m *Manager) DB() *db.DB { return m.db }

// Start 启动 Cron 调度
func (m *Manager) Start() {
	m.scheduleAll()
	m.cron.Start()
	log.Printf("任务调度器已启动")
}

// scheduleAll 为所有任务注册 Cron（先清空已有注册）
func (m *Manager) scheduleAll() {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	cfg := m.cfg.Snapshot()
	for _, id := range m.entryID {
		m.cron.Remove(id)
	}
	m.entryID = map[string]cron.EntryID{}
	for _, t := range cfg.Tasks {
		if t.Crontab == "" {
			continue
		}
		name := t.Name
		id, err := m.cron.AddFunc(t.Crontab, func() {
			if err := m.Run(name); err != nil {
				log.Printf("定时任务 %s 运行失败: %v", name, err)
			}
		})
		if err != nil {
			log.Printf("任务 %s 定时表达式无效 %q: %v", name, t.Crontab, err)
			continue
		}
		m.entryID[name] = id
	}
}

// Reload 配置变更后重新调度
func (m *Manager) Reload() {
	m.scheduleAll()
}

// Run 立即运行任务（异步执行）
func (m *Manager) Run(name string) error { return m.RunPrepared(name, nil) }

// RunPrepared claims exclusive operation ownership before destructive preparation.
func (m *Manager) RunPrepared(name string, prepare func() error) error {
	m.mu.Lock()
	if m.running[name] {
		m.mu.Unlock()
		return fmt.Errorf("任务 %s 正在运行中", name)
	}
	cfg := m.cfg.Snapshot()
	var selected *config.Task
	for i := range cfg.Tasks {
		if cfg.Tasks[i].Name == name {
			selected = &cfg.Tasks[i]
			break
		}
	}
	if selected == nil {
		m.mu.Unlock()
		return fmt.Errorf("任务不存在")
	}
	if err := config.ValidatePlugins(plugins.MergeConfig(cfg.Plugins, selected.Plugins)); err != nil {
		m.mu.Unlock()
		return err
	}
	if err := config.ValidateTask(cfg.STRM.SaveDir, *selected, cfg.Tasks, name); err != nil {
		m.mu.Unlock()
		return err
	}
	dir, err := config.TaskDir(cfg.STRM.SaveDir, name)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.checkActiveDir(dir); err != nil {
		m.mu.Unlock()
		return err
	}
	// Validate local prerequisites before any destructive preparation.
	foundStorage := false
	for _, storage := range cfg.Storages {
		if storage.Name == selected.Storage {
			foundStorage = true
			if _, err := driver.New(storage.Driver, storage.URL, storage.Token); err != nil {
				m.mu.Unlock()
				return err
			}
			break
		}
	}
	if !foundStorage {
		m.mu.Unlock()
		return fmt.Errorf("存储 %s 不存在", selected.Storage)
	}
	var runID int64
	if m.db != nil {
		var err error
		runID, err = m.db.CreateRun(name)
		if err != nil {
			m.mu.Unlock()
			return fmt.Errorf("创建运行记录失败: %w", err)
		}
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			if m.db != nil && runID > 0 {
				if finishErr := m.db.FinishRun(runID, "error", 0, 0, 0, 0, err.Error()); finishErr != nil {
					err = fmt.Errorf("%w; 结束运行记录失败: %v", err, finishErr)
				}
			}
			m.mu.Unlock()
			return err
		}
	}
	logbuf := NewLogBuffer(logBufferMax)
	logbuf.retain = true
	if m.db != nil {
		logbuf.persist = func(lines []LogLine) error { return persistRunLogs(m.db, runID, lines) }
	}
	if old := m.logs[name]; old != nil {
		logbuf.seq = old.LastSeq()
		old.Freeze()
	}
	m.logs[name] = logbuf

	ctx, cancel := context.WithCancel(context.Background())
	m.activeDirs[name] = dir
	m.running[name] = true
	m.cancel[name] = cancel
	m.results[name] = &Result{Running: true, Start: time.Now()}
	m.mu.Unlock()
	m.notify()

	go func() {
		r := &Result{}
		defer func() {
			if p := recover(); p != nil {
				r.Error = fmt.Sprintf("任务内部错误: %v", p)
				logbuf.Logf("ERROR", "%s", r.Error)
			}
			stopped := ctx.Err() != nil
			cancel()
			logbuf.Logf("INFO", "任务 %s 运行完成", name)
			lines := logbuf.Freeze()
			if m.db != nil && runID > 0 {
				if err := persistRunLogs(m.db, runID, lines); err != nil {
					r.Error += fmt.Sprintf("; 保存运行日志失败: %v", err)
					log.Printf("%s", r.Error)
				}
				status := "success"
				if r.Error != "" {
					status = "error"
				} else if stopped {
					status = "stopped"
				}
				counts := r.Result
				if counts == nil {
					counts = &generator.Result{}
				}
				if err := m.db.FinishRun(runID, status, counts.Generated, counts.Copied, counts.Removed, counts.Skipped+counts.SkippedDirs, r.Error); err != nil {
					r.Error += fmt.Sprintf("; 保存运行结果失败: %v", err)
					log.Printf("%s", r.Error)
				}
			}
			// Publish idle only AFTER logs are frozen and database finalization has completed.
			m.mu.Lock()
			start := m.results[name].Start
			delete(m.cancel, name)
			delete(m.activeDirs, name)
			m.running[name] = false
			m.results[name] = &Result{Start: start, End: time.Now(), Result: r.Result, Error: r.Error}
			m.mu.Unlock()
			m.notify()
		}()
		r = m.runTaskConfig(ctx, name, logbuf, cfg)
	}()

	return nil
}

// Stop 停止正在运行的任务（通过 context 取消，扫描中的网络请求一并中断）
// 返回前等待任务收尾完成（running 清零），保证调用方刷新状态时已停止
func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	cancel, ok := m.cancel[name]
	b := m.logs[name] // 锁内取引用，避免与 Log() 写 map 并发
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("任务 %s 未在运行", name)
	}
	m.mu.Unlock() // 先释放锁再取消，避免取消回调与任务收尾相互等待
	cancel()
	if b != nil {
		b.Logf("WARN", "任务 %s 收到停止指令", name)
	}
	// 等待 goroutine 收尾（ctx 取消会立即中断网络请求，正常亚秒级完成）
	deadline := time.Now().Add(5 * time.Second)
	for m.IsRunning(name) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if m.IsRunning(name) {
		return fmt.Errorf("任务 %s 停止指令已发出，但仍在收尾", name)
	}
	return nil
}

// runTask 执行任务（内部）
func (m *Manager) runTask(ctx context.Context, name string, logbuf *LogBuffer) *Result {
	return m.runTaskConfig(ctx, name, logbuf, m.cfg.Snapshot())
}

func (m *Manager) runTaskConfig(ctx context.Context, name string, logbuf *LogBuffer, cfg *config.Config) *Result {
	res := &Result{}

	// 值拷贝：运行期间不受 addTask/updateTask/deleteTask 修改共享配置的影响
	var taskCfg config.Task
	found := false
	for i := range cfg.Tasks {
		if cfg.Tasks[i].Name == name {
			taskCfg = cfg.Tasks[i]
			found = true
			break
		}
	}
	if !found {
		res.Error = "任务不存在"
		return res
	}
	var storageCfg config.Storage
	found = false
	for i := range cfg.Storages {
		if cfg.Storages[i].Name == taskCfg.Storage {
			storageCfg = cfg.Storages[i]
			found = true
			break
		}
	}
	if !found {
		res.Error = fmt.Sprintf("存储 %s 不存在", taskCfg.Storage)
		return res
	}

	drv, err := driver.New(storageCfg.Driver, storageCfg.URL, storageCfg.Token)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	gen := generator.New(cfg, &taskCfg, drv, m.db)
	gen.SetLogger(logbuf)
	res.Result = gen.Run(ctx)
	if ctx.Err() != nil {
		res.Error = "任务已停止"
		logbuf.Logf("WARN", "任务 %s 已停止", name)
		return res
	}
	if len(res.Result.Errors) > 0 {
		res.Error = res.Result.Errors[0]
	}
	return res
}

// Log 返回任务日志缓冲
func (m *Manager) Log(name string) *LogBuffer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.logs[name]; ok {
		return b
	}
	b := NewLogBuffer(logBufferMax)
	m.logs[name] = b
	return b
}

// RunAll 运行所有任务
func (m *Manager) RunAll() []string {
	var failed []string
	for _, t := range m.cfg.Snapshot().Tasks {
		if err := m.Run(t.Name); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", t.Name, err))
		}
	}
	return failed
}

// NextRuns 返回各任务下次运行时间
func (m *Manager) NextRuns() map[string]time.Time {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	out := map[string]time.Time{}
	for name, id := range m.entryID {
		e := m.cron.Entry(id)
		out[name] = e.Next
	}
	return out
}

// Status 返回任务状态（任务名 → 状态）
func (m *Manager) Status() map[string]*Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]*Result{}
	for name, r := range m.results {
		copy := *r
		out[name] = &copy
	}
	return out
}

// IsRunning 任务是否在运行
func (m *Manager) IsRunning(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[name]
}

// SetOnState 设置状态变化回调
func (m *Manager) SetOnState(f func()) { m.mu.Lock(); m.onState = f; m.mu.Unlock() }

func (m *Manager) notify() {
	m.mu.Lock()
	f := m.onState
	m.mu.Unlock()
	if f != nil {
		f()
	}
}

// checkActiveDir is called with m.mu held; reservations survive task edits/deletion.
func (m *Manager) checkActiveDir(dir string) error {
	for name, active := range m.activeDirs {
		for _, pair := range [][2]string{{dir, active}, {active, dir}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				return fmt.Errorf("目标目录与运行中任务 %s 重叠", name)
			}
		}
	}
	return nil
}

// WithIdle serializes filesystem tools with Run/RunPrepared and each other.
func (m *Manager) WithIdle(name string, fn func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running[name] {
		return fmt.Errorf("任务 %s 正在运行中", name)
	}
	cfg := m.cfg.Snapshot()
	for _, t := range cfg.Tasks {
		if t.Name == name {
			if err := config.ValidateTask(cfg.STRM.SaveDir, t, cfg.Tasks, name); err != nil {
				return err
			}
			dir, err := config.TaskDir(cfg.STRM.SaveDir, name)
			if err != nil {
				return err
			}
			if err := m.checkActiveDir(dir); err != nil {
				return err
			}
			return fn()
		}
	}
	return fmt.Errorf("任务不存在")
}

// Freeze seals a run and returns all not-yet-persisted lines, independent of the live ring.
func (b *LogBuffer) Freeze() []LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frozen = true
	for id, ch := range b.subs {
		close(ch)
		delete(b.subs, id)
	}
	lines := append([]LogLine(nil), b.archive...)
	b.archive = nil
	b.retain = false
	return lines
}
