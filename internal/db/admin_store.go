package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// ============ admin（账号密码，bcrypt 存储） ============

const adminTables = `
CREATE TABLE IF NOT EXISTS admin (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	username TEXT NOT NULL,
	password_hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// MigrateAdminTables 创建账号/设置表
func (d *DB) MigrateAdminTables() error {
	if _, err := d.conn.Exec(adminTables); err != nil {
		return fmt.Errorf("建账号表失败: %w", err)
	}
	return nil
}

// Admin 账号信息
type Admin struct {
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
}

// LoadAdmin 读取账号；不存在返回 false
func (d *DB) LoadAdmin() (Admin, bool, error) {
	var a Admin
	err := d.conn.QueryRow(`SELECT username, password_hash FROM admin WHERE id=1`).Scan(&a.Username, &a.PasswordHash)
	if err == sql.ErrNoRows {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	return a, true, nil
}

// SetAdmin 设置账号与密码（bcrypt 哈希存储）
func (d *DB) SetAdmin(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = d.conn.Exec(`INSERT INTO admin (id, username, password_hash) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET username=excluded.username, password_hash=excluded.password_hash`,
		username, string(hash))
	return err
}

// VerifyPassword 校验密码
func (d *DB) VerifyPassword(password string) bool {
	a, ok, err := d.LoadAdmin()
	if err != nil || !ok {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(password)) == nil
}

// RandomPassword 生成随机密码
func RandomPassword() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) // 24 位十六进制
}

// ============ settings（strm/plugins 等模块配置，JSON 存储） ============

// GetSetting 读取设置（JSON 字符串）
func (d *DB) GetSetting(key string) (string, bool, error) {
	var v string
	err := d.conn.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting 保存设置（JSON 字符串）
func (d *DB) SetSetting(key, value string) error {
	_, err := d.conn.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetSettingJSON 读取并解析 JSON 设置
func GetSettingJSON[T any](d *DB, key string, def T) (T, error) {
	v, ok, err := d.GetSetting(key)
	if err != nil {
		return def, err
	}
	if !ok || v == "" {
		return def, nil
	}
	if err := json.Unmarshal([]byte(v), &def); err != nil {
		return def, err
	}
	return def, nil
}

// SetSettingJSON 序列化并保存设置
func SetSettingJSON(d *DB, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return d.SetSetting(key, string(b))
}
