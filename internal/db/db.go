// Package db SQLite 存储层：运行记录、任务日志、审计日志
package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DB SQLite 封装
type DB struct {
	conn *sql.DB
}

// timeFmt 统一时间存储格式（SQLite date() 可解析的 ISO 格式）
const timeFmt = "2006-01-02 15:04:05"

// Run 运行记录
type Run struct {
	ID        int64     `json:"id"`
	Task      string    `json:"task"`
	StartAt   time.Time `json:"start_at"`
	EndAt     time.Time `json:"end_at"`
	Status    string    `json:"status"` // running / success / stopped / error
	Generated int       `json:"generated"`
	Copied    int       `json:"copied"`
	Removed   int       `json:"removed"`
	Skipped   int       `json:"skipped"`
	Error     string    `json:"error"`
}

// Audit 审计记录
type Audit struct {
	ID     int64     `json:"id"`
	Time   time.Time `json:"time"`
	User   string    `json:"user"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
}

// WebhookLog Webhook 处理日志（收到的通知 / 执行动作 / 结果）
type WebhookLog struct {
	ID         int64     `json:"id"`
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`        // emby / task
	Event      string    `json:"event"`       // Emby 事件类型或 task_trigger
	Payload    string    `json:"payload"`     // 收到的完整通知 JSON（截断）
	Action     string    `json:"action"`      // emby_delete / emby_delete_failed / task_trigger
	Target     string    `json:"target"`      // Item.Path 或任务名
	RemotePath string    `json:"remote_path"` // 映射后的远端路径
	Result     string    `json:"result"`      // ok / failed / skipped
	Detail     string    `json:"detail"`      // 错误信息或补充
}

// Open 打开（或创建）数据库并建表
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite 单写者，串行化避免锁冲突
	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		return nil, err
	}
	return db, nil
}

// Close 关闭数据库
func (d *DB) Close() error { return d.conn.Close() }

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task TEXT NOT NULL,
			start_at DATETIME NOT NULL,
			end_at DATETIME,
			status TEXT NOT NULL DEFAULT 'running',
			generated INTEGER NOT NULL DEFAULT 0,
			copied INTEGER NOT NULL DEFAULT 0,
			removed INTEGER NOT NULL DEFAULT 0,
			skipped INTEGER NOT NULL DEFAULT 0,
			error TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_task_time ON runs(task, start_at)`,
		`CREATE TABLE IF NOT EXISTS task_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL,
			time DATETIME NOT NULL,
			level TEXT NOT NULL,
			msg TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_logs_run ON task_logs(run_id)`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time DATETIME NOT NULL,
			user TEXT NOT NULL,
			action TEXT NOT NULL,
			target TEXT,
			detail TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_logs(time)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action)`,
		`CREATE TABLE IF NOT EXISTS webhook_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time DATETIME NOT NULL,
			kind TEXT NOT NULL,
			event TEXT NOT NULL,
			payload TEXT,
			action TEXT NOT NULL,
			target TEXT,
			remote_path TEXT,
			result TEXT NOT NULL,
			detail TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_webhook_logs_time ON webhook_logs(time)`,
	}
	for _, s := range stmts {
		if _, err := d.conn.Exec(s); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	if err := d.MigrateConfigTables(); err != nil {
		return err
	}
	return d.MigrateAdminTables()
}

// ============ runs ============

// CreateRun 创建运行记录，返回 ID
func (d *DB) CreateRun(task string) (int64, error) {
	res, err := d.conn.Exec(`INSERT INTO runs (task, start_at, status) VALUES (?, ?, 'running')`, task, time.Now().Format(timeFmt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRun 结束运行记录
func (d *DB) FinishRun(id int64, status string, generated, copied, removed, skipped int, errMsg string) error {
	_, err := d.conn.Exec(`UPDATE runs SET end_at=?, status=?, generated=?, copied=?, removed=?, skipped=?, error=? WHERE id=?`,
		time.Now().Format(timeFmt), status, generated, copied, removed, skipped, nullStr(errMsg), id)
	return err
}

// UpdateRunStatus 仅更新状态（停止时）
func (d *DB) UpdateRunStatus(id int64, status, errMsg string) error {
	_, err := d.conn.Exec(`UPDATE runs SET end_at=?, status=?, error=? WHERE id=?`, time.Now().Format(timeFmt), status, nullStr(errMsg), id)
	return err
}

// CleanupStaleRuns 清理上次异常退出遗留的 running 记录（程序被强制终止时无法收尾）
func (d *DB) CleanupStaleRuns() error {
	_, err := d.conn.Exec(`UPDATE runs SET end_at=?, status='error', error='程序异常退出，运行中断'
		WHERE status='running'`, time.Now().Format(timeFmt))
	return err
}

// DeleteDirCache 删除任务的目录时间缓存（全量覆写时强制重新扫描）
func (d *DB) DeleteDirCache(task string) error {
	_, err := d.conn.Exec(`DELETE FROM dir_cache WHERE task=?`, task)
	return err
}

// InsertLogs 批量写入任务日志
func (d *DB) InsertLogs(runID int64, lines []struct {
	Time  time.Time
	Level string
	Msg   string
}) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO task_logs (run_id, time, level, msg) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, l := range lines {
		if _, err := stmt.Exec(runID, l.Time.Format(timeFmt), l.Level, l.Msg); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecentRuns 最近运行记录（按时间倒序）
func (d *DB) RecentRuns(limit int) ([]Run, error) {
	rows, err := d.conn.Query(`SELECT id, task, start_at, end_at, status, generated, copied, removed, skipped, COALESCE(error,'')
		FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRuns(rows)
}

// TaskRuns 某任务的运行历史
func (d *DB) TaskRuns(task string, limit int) ([]Run, error) {
	rows, err := d.conn.Query(`SELECT id, task, start_at, end_at, status, generated, copied, removed, skipped, COALESCE(error,'')
		FROM runs WHERE task=? ORDER BY id DESC LIMIT ?`, task, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRuns(rows)
}

// RunLogs 某次运行的完整日志
func (d *DB) RunLogs(runID int64) ([]struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}, error) {
	rows, err := d.conn.Query(`SELECT time, level, msg FROM task_logs WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]struct {
		Time  time.Time `json:"time"`
		Level string    `json:"level"`
		Msg   string    `json:"msg"`
	}, 0)
	for rows.Next() {
		var l struct {
			Time  time.Time `json:"time"`
			Level string    `json:"level"`
			Msg   string    `json:"msg"`
		}
		if err := rows.Scan(&l.Time, &l.Level, &l.Msg); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func scanRuns(rows *sql.Rows) ([]Run, error) {
	out := make([]Run, 0)
	for rows.Next() {
		var r Run
		var errMsg sql.NullString
		var start, end sql.NullTime
		if err := rows.Scan(&r.ID, &r.Task, &start, &end, &r.Status,
			&r.Generated, &r.Copied, &r.Removed, &r.Skipped, &errMsg); err != nil {
			return nil, err
		}
		r.Error = errMsg.String
		r.StartAt = start.Time
		r.EndAt = end.Time
		out = append(out, r)
	}
	return out, nil
}

// ============ audit ============

// InsertAudit 写入审计
func (d *DB) InsertAudit(user, action, target, detail string) error {
	_, err := d.conn.Exec(`INSERT INTO audit_logs (time, user, action, target, detail) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Format(timeFmt), user, action, nullStr(target), nullStr(detail))
	return err
}

// RecentAudits 最近审计记录
func (d *DB) RecentAudits(limit int) ([]Audit, error) {
	rows, err := d.conn.Query(`SELECT id, time, user, action, COALESCE(target,''), COALESCE(detail,'')
		FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Audit, 0)
	for rows.Next() {
		var a Audit
		if err := rows.Scan(&a.ID, &a.Time, &a.User, &a.Action, &a.Target, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// ============ webhook 日志 ============

// webhookPayloadMax 入库 payload 截断长度，防止超大通知撑爆数据库
const webhookPayloadMax = 2048

// InsertWebhookLog 写入 webhook 处理日志
func (d *DB) InsertWebhookLog(l WebhookLog) error {
	if len(l.Payload) > webhookPayloadMax {
		l.Payload = l.Payload[:webhookPayloadMax] + "…"
	}
	_, err := d.conn.Exec(`INSERT INTO webhook_logs (time, kind, event, payload, action, target, remote_path, result, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Format(timeFmt), l.Kind, l.Event, nullStr(l.Payload), l.Action,
		nullStr(l.Target), nullStr(l.RemotePath), l.Result, nullStr(l.Detail))
	return err
}

// RecentWebhookLogs 最近 webhook 日志
func (d *DB) RecentWebhookLogs(limit int) ([]WebhookLog, error) {
	rows, err := d.conn.Query(`SELECT id, time, kind, event, COALESCE(payload,''), action,
		COALESCE(target,''), COALESCE(remote_path,''), result, COALESCE(detail,'')
		FROM webhook_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WebhookLog, 0)
	for rows.Next() {
		var l WebhookLog
		if err := rows.Scan(&l.ID, &l.Time, &l.Kind, &l.Event, &l.Payload, &l.Action,
			&l.Target, &l.RemotePath, &l.Result, &l.Detail); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
