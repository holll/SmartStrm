package db

import "database/sql"

// LoadDirCacheNamespace activates the current fingerprint transactionally. Changing
// A -> B -> A must rescan too: output may have changed while B was active.
func (d *DB) LoadDirCacheNamespace(task, namespace string) (map[string]int64, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	key := "dir_cache_namespace/" + task
	var previous string
	err = tx.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == sql.ErrNoRows || previous != namespace {
		if _, err = tx.Exec(`DELETE FROM dir_cache WHERE task=? OR instr(task, ?)=1`, task, task+"\x00"); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(`INSERT INTO settings (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, namespace); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(`SELECT remote_path,mtime FROM dir_cache WHERE task=?`, namespace)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for rows.Next() {
		var path string
		var mtime int64
		if err = rows.Scan(&path, &mtime); err != nil {
			rows.Close()
			return nil, err
		}
		out[path] = mtime
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// SaveDirCacheNamespace discards late writers for an already superseded config.
// The fingerprint guard and cache writes share a transaction, so neither can race
// with activation of the next configuration.
func (d *DB) SaveDirCacheNamespace(task, namespace string, cache map[string]int64) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRow(`SELECT value FROM settings WHERE key=?`, "dir_cache_namespace/"+task).Scan(&current)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if current != namespace {
		return nil
	}
	stmt, err := tx.Prepare(`INSERT INTO dir_cache(task,remote_path,mtime) VALUES (?,?,?) ON CONFLICT(task,remote_path) DO UPDATE SET mtime=excluded.mtime`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for path, mtime := range cache {
		if _, err = stmt.Exec(namespace, path, mtime); err != nil {
			return err
		}
	}
	return tx.Commit()
}
