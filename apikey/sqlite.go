package apikey

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const selectKeyColumns = `
	id, name, description, key_prefix, key_hash, hash_algorithm, status,
	created_at, updated_at, last_used_at, expires_at, revoked_at, version`

type SQLiteRepository struct {
	db *sql.DB
}

func OpenSQLite(ctx context.Context, path string) (*SQLiteRepository, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("MCP_API_KEY_DB_PATH 不可為空")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("無法解析 MCP_API_KEY_DB_PATH")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, errors.New("無法建立 API key 資料庫目錄")
	}
	_ = os.Chmod(filepath.Dir(abs), 0o700)

	dsn := "file:" + filepath.ToSlash(abs) +
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.New("無法開啟 API key 資料庫")
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	repo := &SQLiteRepository{db: db}
	if err := repo.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(abs, 0o600)
	return repo, nil
}

func (r *SQLiteRepository) Close() error {
	return r.db.Close()
}

func (r *SQLiteRepository) Migrate(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("無法開始 API key migration")
	}
	defer tx.Rollback()

	const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_api_key_meta (
	name TEXT PRIMARY KEY,
	value BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_api_keys (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	key_prefix TEXT NOT NULL UNIQUE,
	key_hash BLOB NOT NULL,
	hash_algorithm TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('active','disabled','revoked')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	last_used_at TEXT NULL,
	expires_at TEXT NULL,
	revoked_at TEXT NULL,
	deleted_at TEXT NULL,
	version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0)
);
CREATE INDEX IF NOT EXISTS idx_mcp_api_keys_active
	ON mcp_api_keys(status, deleted_at, expires_at);
CREATE TABLE IF NOT EXISTS mcp_api_key_audit (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	key_id TEXT NOT NULL,
	key_name TEXT NOT NULL,
	key_prefix TEXT NOT NULL,
	action TEXT NOT NULL,
	result TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_api_key_audit_key
	ON mcp_api_key_audit(key_id, created_at);
`
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return errors.New("API key database migration 失敗")
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)`,
		dbTime(time.Now())); err != nil {
		return errors.New("無法記錄 API key database migration")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("API key database migration commit 失敗")
	}
	return nil
}

func (r *SQLiteRepository) EnsurePepper(ctx context.Context, check []byte) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("無法開始 pepper 驗證")
	}
	defer tx.Rollback()
	var stored []byte
	err = tx.QueryRowContext(ctx,
		`SELECT value FROM mcp_api_key_meta WHERE name = 'pepper_check'`).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO mcp_api_key_meta(name, value) VALUES('pepper_check', ?)`, check); err != nil {
			return errors.New("無法寫入 pepper 驗證資料")
		}
	case err != nil:
		return errors.New("無法讀取 pepper 驗證資料")
	case !secureDigestEqual(stored, check):
		return errors.New("MCP_API_KEY_PEPPER 與既有資料庫不相符")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("pepper 驗證 transaction commit 失敗")
	}
	return nil
}

func (r *SQLiteRepository) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mcp_api_keys WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		return 0, errors.New("無法讀取 API key 數量")
	}
	return n, nil
}

func (r *SQLiteRepository) List(ctx context.Context) ([]APIKey, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+selectKeyColumns+
		` FROM mcp_api_keys WHERE deleted_at IS NULL ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, errors.New("無法讀取 API key 清單")
	}
	defer rows.Close()

	var out []APIKey
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, errors.New("無法解析 API key 資料")
		}
		out = append(out, rec.APIKey)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("讀取 API key 清單失敗")
	}
	return out, nil
}

func (r *SQLiteRepository) Get(ctx context.Context, id string) (APIKey, error) {
	rec, err := scanRecord(r.db.QueryRowContext(ctx, `SELECT `+selectKeyColumns+
		` FROM mcp_api_keys WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, errors.New("無法讀取 API key")
	}
	return rec.APIKey, nil
}

func (r *SQLiteRepository) ListActive(ctx context.Context) ([]credential, error) {
	return listActive(ctx, r.db)
}

func (r *SQLiteRepository) Create(ctx context.Context, rec keyRecord, action string) ([]credential, error) {
	return r.mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO mcp_api_keys(
	id, name, description, key_prefix, key_hash, hash_algorithm, status,
	created_at, updated_at, expires_at, version
) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			rec.ID, rec.Name, rec.Description, rec.Prefix, rec.Hash, rec.HashAlgorithm,
			rec.Status, dbTime(rec.CreatedAt), dbTime(rec.UpdatedAt), nullableDBTime(rec.ExpiresAt), rec.Version)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return ErrConflict
			}
			return errors.New("無法建立 API key")
		}
		return insertAudit(ctx, tx, AuditEvent{
			KeyID: rec.ID, KeyName: rec.Name, KeyPrefix: rec.Prefix,
			Action: action, Result: "success", CreatedAt: rec.CreatedAt,
		})
	})
}

func (r *SQLiteRepository) UpdateMetadata(
	ctx context.Context,
	id string,
	version int64,
	name string,
	description string,
	expiresAt *time.Time,
	now time.Time,
) ([]credential, error) {
	return r.mutate(ctx, func(tx *sql.Tx) error {
		current, err := getRecordTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrConflict
		}
		res, err := tx.ExecContext(ctx, `
UPDATE mcp_api_keys
SET name = ?, description = ?, expires_at = ?, updated_at = ?, version = version + 1
WHERE id = ? AND version = ? AND deleted_at IS NULL`,
			name, description, nullableDBTime(expiresAt), dbTime(now), id, version)
		if err != nil {
			return errors.New("無法更新 API key")
		}
		if err := requireOneRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, AuditEvent{
			KeyID: id, KeyName: name, KeyPrefix: current.Prefix,
			Action: "metadata_updated", Result: "success", CreatedAt: now,
		})
	})
}

func (r *SQLiteRepository) SetStatus(
	ctx context.Context,
	id string,
	version int64,
	status Status,
	now time.Time,
) ([]credential, error) {
	return r.mutate(ctx, func(tx *sql.Tx) error {
		current, err := getRecordTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrConflict
		}
		if status == StatusDisabled && currentlyActive(current, now) {
			if err := ensureAnotherActive(ctx, tx, id, now); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `
UPDATE mcp_api_keys
SET status = ?, revoked_at = NULL, updated_at = ?, version = version + 1
WHERE id = ? AND version = ? AND deleted_at IS NULL`,
			status, dbTime(now), id, version)
		if err != nil {
			return errors.New("無法更新 API key 狀態")
		}
		if err := requireOneRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, AuditEvent{
			KeyID: id, KeyName: current.Name, KeyPrefix: current.Prefix,
			Action: statusAction(status), Result: "success", CreatedAt: now,
		})
	})
}

func statusAction(status Status) string {
	if status == StatusActive {
		return "enabled"
	}
	return "disabled"
}

func (r *SQLiteRepository) Rotate(
	ctx context.Context,
	id string,
	version int64,
	newPrefix string,
	newHash []byte,
	now time.Time,
) ([]credential, error) {
	return r.mutate(ctx, func(tx *sql.Tx) error {
		current, err := getRecordTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrConflict
		}
		res, err := tx.ExecContext(ctx, `
UPDATE mcp_api_keys
SET key_prefix = ?, key_hash = ?, hash_algorithm = ?, status = ?,
	revoked_at = NULL, updated_at = ?, version = version + 1
WHERE id = ? AND version = ? AND deleted_at IS NULL`,
			newPrefix, newHash, HashAlgorithm, StatusActive, dbTime(now), id, version)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return ErrConflict
			}
			return errors.New("無法輪替 API key")
		}
		if err := requireOneRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, AuditEvent{
			KeyID: id, KeyName: current.Name, KeyPrefix: newPrefix,
			Action: "rotated", Result: "success", CreatedAt: now,
		})
	})
}

func (r *SQLiteRepository) Delete(
	ctx context.Context,
	id string,
	version int64,
	now time.Time,
) ([]credential, error) {
	return r.mutate(ctx, func(tx *sql.Tx) error {
		current, err := getRecordTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrConflict
		}
		if currentlyActive(current, now) {
			if err := ensureAnotherActive(ctx, tx, id, now); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `
UPDATE mcp_api_keys
SET status = ?, revoked_at = ?, deleted_at = ?, updated_at = ?, version = version + 1
WHERE id = ? AND version = ? AND deleted_at IS NULL`,
			StatusRevoked, dbTime(now), dbTime(now), dbTime(now), id, version)
		if err != nil {
			return errors.New("無法刪除 API key")
		}
		if err := requireOneRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, AuditEvent{
			KeyID: id, KeyName: current.Name, KeyPrefix: current.Prefix,
			Action: "deleted", Result: "success", CreatedAt: now,
		})
	})
}

func (r *SQLiteRepository) UpdateLastUsed(ctx context.Context, used map[string]time.Time) error {
	if len(used) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("無法開始 last-used transaction")
	}
	defer tx.Rollback()
	for id, at := range used {
		if _, err := tx.ExecContext(ctx, `
UPDATE mcp_api_keys
SET last_used_at = CASE
	WHEN last_used_at IS NULL OR last_used_at < ? THEN ?
	ELSE last_used_at
END
WHERE id = ? AND deleted_at IS NULL`, dbTime(at), dbTime(at), id); err != nil {
			return errors.New("無法更新 API key 最後使用時間")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("last-used transaction commit 失敗")
	}
	return nil
}

func (r *SQLiteRepository) ListAudit(ctx context.Context, keyID string) ([]AuditEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT key_id, key_name, key_prefix, action, result, created_at
FROM mcp_api_key_audit WHERE key_id = ? ORDER BY id`, keyID)
	if err != nil {
		return nil, errors.New("無法讀取 API key audit")
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var created string
		if err := rows.Scan(&event.KeyID, &event.KeyName, &event.KeyPrefix,
			&event.Action, &event.Result, &created); err != nil {
			return nil, errors.New("無法解析 API key audit")
		}
		event.CreatedAt, err = parseDBTime(created)
		if err != nil {
			return nil, errors.New("無法解析 API key audit 時間")
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) mutate(
	ctx context.Context,
	fn func(*sql.Tx) error,
) ([]credential, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.New("無法開始 API key transaction")
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return nil, err
	}
	active, err := listActive(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, errors.New("API key transaction commit 失敗")
	}
	return active, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listActive(ctx context.Context, q queryer) ([]credential, error) {
	rows, err := q.QueryContext(ctx, `
SELECT id, key_prefix, key_hash, expires_at
FROM mcp_api_keys
WHERE status = 'active' AND deleted_at IS NULL AND revoked_at IS NULL`)
	if err != nil {
		return nil, errors.New("無法讀取啟用中的 API key")
	}
	defer rows.Close()
	now := time.Now().UTC()
	var out []credential
	for rows.Next() {
		var item credential
		var expires sql.NullString
		if err := rows.Scan(&item.ID, &item.Prefix, &item.Hash, &expires); err != nil {
			return nil, errors.New("無法解析啟用中的 API key")
		}
		if expires.Valid {
			t, err := parseDBTime(expires.String)
			if err != nil {
				return nil, errors.New("API key 到期時間格式錯誤")
			}
			item.ExpiresAt = &t
			if !t.After(now) {
				continue
			}
		}
		item.Hash = append([]byte(nil), item.Hash...)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("讀取啟用中的 API key 失敗")
	}
	return out, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanRecord(row rowScanner) (keyRecord, error) {
	var rec keyRecord
	var created, updated string
	var lastUsed, expires, revoked sql.NullString
	err := row.Scan(
		&rec.ID, &rec.Name, &rec.Description, &rec.Prefix, &rec.Hash,
		&rec.HashAlgorithm, &rec.Status, &created, &updated,
		&lastUsed, &expires, &revoked, &rec.Version,
	)
	if err != nil {
		return keyRecord{}, err
	}
	rec.CreatedAt, err = parseDBTime(created)
	if err != nil {
		return keyRecord{}, err
	}
	rec.UpdatedAt, err = parseDBTime(updated)
	if err != nil {
		return keyRecord{}, err
	}
	if rec.LastUsedAt, err = parseNullableTime(lastUsed); err != nil {
		return keyRecord{}, err
	}
	if rec.ExpiresAt, err = parseNullableTime(expires); err != nil {
		return keyRecord{}, err
	}
	if rec.RevokedAt, err = parseNullableTime(revoked); err != nil {
		return keyRecord{}, err
	}
	rec.MaskedKey = maskPrefix(rec.Prefix)
	rec.Hash = append([]byte(nil), rec.Hash...)
	return rec, nil
}

func getRecordTx(ctx context.Context, tx *sql.Tx, id string) (keyRecord, error) {
	rec, err := scanRecord(tx.QueryRowContext(ctx, `SELECT `+selectKeyColumns+
		` FROM mcp_api_keys WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return keyRecord{}, ErrNotFound
	}
	if err != nil {
		return keyRecord{}, errors.New("無法讀取 API key")
	}
	return rec, nil
}

func ensureAnotherActive(ctx context.Context, tx *sql.Tx, excludedID string, now time.Time) error {
	active, err := listActive(ctx, tx)
	if err != nil {
		return err
	}
	for _, item := range active {
		if item.ID != excludedID && (item.ExpiresAt == nil || item.ExpiresAt.After(now)) {
			return nil
		}
	}
	return ErrLastActive
}

func currentlyActive(rec keyRecord, now time.Time) bool {
	return rec.Status == StatusActive && rec.RevokedAt == nil &&
		(rec.ExpiresAt == nil || rec.ExpiresAt.After(now))
}

func insertAudit(ctx context.Context, tx *sql.Tx, event AuditEvent) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO mcp_api_key_audit(
	key_id, key_name, key_prefix, action, result, created_at
) VALUES(?,?,?,?,?,?)`,
		event.KeyID, event.KeyName, event.KeyPrefix, event.Action, event.Result, dbTime(event.CreatedAt))
	if err != nil {
		return errors.New("無法記錄 API key audit")
	}
	return nil
}

func requireOneRow(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return errors.New("無法確認 API key 更新結果")
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func dbTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func nullableDBTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return dbTime(*t)
}

func parseDBTime(raw string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, raw)
}

func parseNullableTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid {
		return nil, nil
	}
	t, err := parseDBTime(raw.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
