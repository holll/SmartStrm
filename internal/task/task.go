// Package task 任务管理与 Cron 调度
package task

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"smartstrm/internal/config"
	"smartstrm/internal/db"
	"smartstrm/internal/driver"
	"smartstrm/internal/generator"
)

// LogLine 任务日志行
type LogLine struct {
	Seq   int64     `json:"seq"`
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

// LogBuffer 任务日志缓冲（环形，按任务隔离）
type LogBuffer struct {
	mu    sync.Mutex
	lines []LogLine
	max   int
	seq   int64
}

// NewLogBuffer 创建日志缓冲
func NewLogBuffer(max int) *LogBuffer {
	return &LogBuffer{max: max}
}

// Logf 追加日志
func (b *LogBuffer) Logf(level, format string, args ...any) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	b.lines = append(b.lines, LogLine{Seq: b.seq, Time: time.Now(), Level: level, Msg: fmt.Sprintf(format, args...)})
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
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
		return b.lines
	}
	start := 0
	for start < len(b.lines) && b.lines[start].Seq <= seq {
		start++
	}
	return b.lines[start:]
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
	cfg     *config.Config
	db      *db.DB
	cron    *cron.Cron
	entryID map[string]cron.EntryID // 任务名 → Cron 条目
	mu      sync.Mutex
	running map[string]bool   // 正在运行的任务
	cancel  map[string]context.CancelFunc // 运行中任务的取消函数
	results map[string]*Result // 最近一次运行结果
	logs    map[string]*LogBuffer // 任务日志缓冲
	onState func()             // 状态变化回调（管理页轮询用）
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
		cfg:     cfg,
		db:      database,
		cron:    cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger))),
		entryID: map[string]cron.EntryID{},
		running: map[string]bool{},
		cancel:  map[string]context.CancelFunc{},
		results: map[string]*Result{},
		logs:    map[string]*LogBuffer{},
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
	for _, id := range m.entryID {
		m.cron.Remove(id)
	}
	m.entryID = map[string]cron.EntryID{}
	for _, t := range m.cfg.Tasks {
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
func (m *Manager) Run(name string) error {
	m.mu.Lock()
	if m.running[name] {
		m.mu.Unlock()
		return fmt.Errorf("任务 %s 正在运行中", name)
	}
	logbuf := m.logs[name]
	if logbuf == nil {
		logbuf = NewLogBuffer(5000)
		m.logs[name] = logbuf
	}
	// 每次运行重置日志缓冲，实时日志只保留当前（或最近一次）运行的记录；
	// 历史运行日志在数据库中查看（runs/task_logs）
	logbuf.Reset()
	logbuf.Logf("INFO", "==================================================")
	logbuf.Logf("INFO", "任务 %s 开始运行", name)
	startSeq := logbuf.LastSeq() // 本次运行起始 seq，落库时只取增量，避免历史日志重复写入

	// 数据库运行记录
	var runID int64
	if m.db != nil {
		runID, _ = m.db.CreateRun(name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.running[name] = true
	m.cancel[name] = cancel
	m.results[name] = &Result{Running: true, Start: time.Now()}
	m.mu.Unlock()
	m.notify()

	go func() {
		// panic 兜底：任何内部 panic 都必须清理运行状态，否则任务将永远显示"运行中"
		defer func() {
			if p := recover(); p != nil {
				log.Printf("任务 %s panic: %v", name, p)
				logbuf.Logf("ERROR", "任务 %s 内部错误: %v", name, p)
				m.mu.Lock()
				cancel()
				delete(m.cancel, name)
				m.running[name] = false
				if r := m.results[name]; r != nil {
					r.Running = false
					r.End = time.Now()
					r.Error = fmt.Sprintf("任务内部错误: %v", p)
				}
				m.mu.Unlock()
				m.notify()
			}
		}()
		r := m.runTask(ctx, name, logbuf)
		stopped := ctx.Err() != nil // 须在 cancel() 之前判定
		m.mu.Lock()
		cancel()
		delete(m.cancel, name)
		m.running[name] = false
		m.results[name] = &Result{
			Running: false,
			Start:   m.results[name].Start,
			End:     time.Now(),
			Result:  r.Result,
			Error:   r.Error,
		}
		if r.Result != nil && len(r.Result.Errors) > 0 {
			logbuf.Logf("ERROR", "任务 %s 运行完成，%d 个错误", name, len(r.Result.Errors))
		} else {
			logbuf.Logf("INFO", "任务 %s 运行完成", name)
		}
		m.mu.Unlock()

		// 落库：运行结果 + 本次日志（锁外执行）
		if m.db != nil && runID > 0 {
			status := "success"
			if r.Error != "" {
				status = "error"
			}
			if stopped {
				status = "stopped"
			}
			if r.Result != nil {
				_ = m.db.FinishRun(runID, status, r.Result.Generated, r.Result.Copied, r.Result.Removed, r.Result.Skipped, r.Error)
			} else {
				_ = m.db.FinishRun(runID, status, 0, 0, 0, 0, r.Error)
			}
			lines := logbuf.Since(startSeq)
			dbLines := make([]struct {
				Time  time.Time
				Level string
				Msg   string
			}, len(lines))
			for i, l := range lines {
				dbLines[i] = struct {
					Time  time.Time
					Level string
					Msg   string
				}{l.Time, l.Level, l.Msg}
			}
			_ = m.db.InsertLogs(runID, dbLines)
		}
		m.notify()
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
	res := &Result{}
	cfg := m.cfg

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
	b := NewLogBuffer(5000)
	m.logs[name] = b
	return b
}

// RunAll 运行所有任务
func (m *Manager) RunAll() []string {
	var failed []string
	for _, t := range m.cfg.Tasks {
		if err := m.Run(t.Name); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", t.Name, err))
		}
	}
	return failed
}

// NextRuns 返回各任务下次运行时间
func (m *Manager) NextRuns() map[string]time.Time {
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
		out[name] = r
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
func (m *Manager) SetOnState(f func()) { m.onState = f }

func (m *Manager) notify() {
	if m.onState != nil {
		m.onState()
	}
}
