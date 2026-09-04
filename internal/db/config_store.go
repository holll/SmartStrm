package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"

	"smartstrm/internal/config"
)

// ============ 表结构 ============

const configTables = `
CREATE TABLE IF NOT EXISTS storages (
	name TEXT PRIMARY KEY,
	driver TEXT NOT NULL DEFAULT 'openlist',
	url TEXT NOT NULL,
	token TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
	name TEXT PRIMARY KEY,
	storage TEXT NOT NULL,
	storage_path TEXT NOT NULL,
	crontab TEXT,
	incremental INTEGER NOT NULL DEFAULT 1,
	dir_time_check INTEGER NOT NULL DEFAULT 1,
	keep_local_asset INTEGER NOT NULL DEFAULT 0,
	plugins TEXT
);
CREATE TABLE IF NOT EXISTS webhook (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	token TEXT NOT NULL,
	emby_delete_sync TEXT
);
`

// MigrateConfigTables 创建配置表（在 migrate 中调用）
func (d *DB) MigrateConfigTables() error {
	if _, err := d.conn.Exec(configTables); err != nil {
		return fmt.Errorf("建配置表失败: %w", err)
	}
	return nil
}

// ============ storages ============

// ListStorages 读取全部存储
func (d *DB) ListStorages() ([]config.Storage, error) {
	rows, err := d.conn.Query(`SELECT name, driver, url, token FROM storages ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []config.Storage
	for rows.Next() {
		var s config.Storage
		if err := rows.Scan(&s.Name, &s.Driver, &s.URL, &s.Token); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// SaveStorage 新增或更新存储
func (d *DB) SaveStorage(s config.Storage) error {
	_, err := d.conn.Exec(`INSERT INTO storages (name, driver, url, token) VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET driver=excluded.driver, url=excluded.url, token=excluded.token`,
		s.Name, s.Driver, s.URL, s.Token)
	return err
}

// DeleteStorage 删除存储
func (d *DB) DeleteStorage(name string) error {
	_, err := d.conn.Exec(`DELETE FROM storages WHERE name=?`, name)
	return err
}

// ============ tasks ============

// ListTasks 读取全部任务
func (d *DB) ListTasks() ([]config.Task, error) {
	rows, err := d.conn.Query(`SELECT name, storage, storage_path, COALESCE(crontab,''), incremental,
		dir_time_check, keep_local_asset, COALESCE(plugins,'') FROM tasks ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []config.Task
	for rows.Next() {
		var t config.Task
		var pluginsStr string
		var inc, dirCheck, keep int
		if err := rows.Scan(&t.Name, &t.Storage, &t.StoragePath, &t.Crontab, &inc,
			&dirCheck, &keep, &pluginsStr); err != nil {
			return nil, err
		}
		t.Incremental = inc == 1
		t.DirTimeCheck = dirCheck == 1
		t.KeepLocalAst = keep == 1
		if pluginsStr != "" {
			_ = json.Unmarshal([]byte(pluginsStr), &t.Plugins)
		}
		out = append(out, t)
	}
	return out, nil
}

// SaveTask 新增或更新任务
func (d *DB) SaveTask(t config.Task) error {
	pluginsJSON := ""
	if len(t.Plugins) > 0 {
		b, err := json.Marshal(t.Plugins)
		if err != nil {
			return err
		}
		pluginsJSON = string(b)
	}
	_, err := d.conn.Exec(`INSERT INTO tasks (name, storage, storage_path, crontab, incremental, dir_time_check, keep_local_asset, plugins)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET storage=excluded.storage, storage_path=excluded.storage_path,
			crontab=excluded.crontab, incremental=excluded.incremental, dir_time_check=excluded.dir_time_check,
			keep_local_asset=excluded.keep_local_asset, plugins=excluded.plugins`,
		t.Name, t.Storage, t.StoragePath, t.Crontab, boolInt(t.Incremental),
		boolInt(t.DirTimeCheck), boolInt(t.KeepLocalAst), pluginsJSON)
	return err
}

// DeleteTask 删除任务
func (d *DB) DeleteTask(name string) error {
	_, err := d.conn.Exec(`DELETE FROM tasks WHERE name=?`, name)
	return err
}

// ============ webhook ============

// WebhookConfig 持久化的 webhook 配置
type WebhookConfig struct {
	Token          string                `json:"token"`
	EmbyDeleteSync config.EmbyDeleteSync `json:"emby_delete_sync"`
}

// LoadWebhook 读取 webhook 配置；无记录时返回 false
func (d *DB) LoadWebhook() (WebhookConfig, bool, error) {
	var w WebhookConfig
	var embyStr sql.NullString
	err := d.conn.QueryRow(`SELECT token, emby_delete_sync FROM webhook WHERE id=1`).Scan(&w.Token, &embyStr)
	if err == sql.ErrNoRows {
		return w, false, nil
	}
	if err != nil {
		return w, false, err
	}
	if embyStr.Valid && embyStr.String != "" {
		_ = json.Unmarshal([]byte(embyStr.String), &w.EmbyDeleteSync)
	}
	return w, true, nil
}

// SaveWebhook 保存 webhook 配置
func (d *DB) SaveWebhook(w WebhookConfig) error {
	embyJSON := ""
	if b, err := json.Marshal(w.EmbyDeleteSync); err == nil {
		embyJSON = string(b)
	}
	_, err := d.conn.Exec(`INSERT INTO webhook (id, token, emby_delete_sync) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET token=excluded.token, emby_delete_sync=excluded.emby_delete_sync`,
		w.Token, embyJSON)
	return err
}

// RegenerateWebhookToken 重新生成 token（旧的立即失效）
func (d *DB) RegenerateWebhookToken() (string, error) {
	tok := randomToken(16)
	cur, ok, err := d.LoadWebhook()
	if err != nil {
		return "", err
	}
	if !ok {
		cur.Token = tok
	} else {
		cur.Token = tok
	}
	if err := d.SaveWebhook(cur); err != nil {
		return "", err
	}
	return tok, nil
}

// randomToken 随机 token
func randomToken(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("tok%d", n)
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
