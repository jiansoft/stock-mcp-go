package apikey

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testPepper = []byte("test-pepper-for-api-keys-32-bytes")

func newTestRepository(t *testing.T) *SQLiteRepository {
	t.Helper()
	repo, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatalf("OpenSQLite(): %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func newTestService(t *testing.T, legacy string) (*Service, *SQLiteRepository) {
	t.Helper()
	repo := newTestRepository(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := NewService(t.Context(), repo, testPepper, legacy, logger)
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, repo
}

func TestLegacyMigration(t *testing.T) {
	const legacy = "old-production-key-that-is-not-new-format"
	service, repo := newTestService(t, legacy)

	if _, ok := service.Authenticate(t.Context(), legacy); !ok {
		t.Fatal("匯入的 MCP_API_KEY 應立即可用")
	}
	items, err := service.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "Migrated MCP_API_KEY" {
		t.Fatalf("匯入項目不正確:%+v", items)
	}
	if items[0].MaskedKey == legacy {
		t.Fatal("清單不可回傳完整 legacy key")
	}
	audit, err := repo.ListAudit(t.Context(), items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 || audit[0].Action != "migration_imported" {
		t.Fatalf("migration audit 不正確:%+v", audit)
	}

	// 資料庫已有資料時不可重複匯入。
	_ = service.Close()
	reopened, err := NewService(t.Context(), repo, testPepper, "another-legacy-key",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err = reopened.List(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("不應重複匯入:%+v err=%v", items, err)
	}
}

func TestImmediateLifecycle(t *testing.T) {
	service, _ := newTestService(t, "")
	ctx := t.Context()

	keyA, fullA, err := service.Create(ctx, CreateInput{Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(ctx, fullA); !ok {
		t.Fatal("建立後應立即可用")
	}
	keyB, fullB, err := service.Create(ctx, CreateInput{Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(ctx, fullB); !ok {
		t.Fatal("第二組 key 應可用")
	}

	keyA, err = service.Disable(ctx, keyA.ID, keyA.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(ctx, fullA); ok {
		t.Fatal("停用後舊 key 必須立即失效")
	}

	keyA, err = service.Enable(ctx, keyA.ID, keyA.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(ctx, fullA); !ok {
		t.Fatal("啟用後 key 應立即恢復")
	}

	keyA, rotated, err := service.Rotate(ctx, keyA.ID, keyA.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(ctx, fullA); ok {
		t.Fatal("輪替後舊 key 必須立即失效")
	}
	if _, ok := service.Authenticate(ctx, rotated); !ok {
		t.Fatal("輪替後新 key 應立即可用")
	}

	if err := service.Delete(ctx, keyA.ID, keyA.Version); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(ctx, rotated); ok {
		t.Fatal("刪除後 key 必須立即失效")
	}
	if _, ok := service.Authenticate(ctx, fullB); !ok {
		t.Fatal("其他 active key 不應受影響")
	}

	if _, err := service.Get(ctx, keyB.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseNeverStoresRecoverableFullKeyOrPepper(t *testing.T) {
	service, repo := newTestService(t, "")
	item, full, err := service.Create(t.Context(), CreateInput{Name: "secret storage"})
	if err != nil {
		t.Fatal(err)
	}
	var prefix string
	var hash []byte
	if err := repo.db.QueryRowContext(t.Context(),
		`SELECT key_prefix, key_hash FROM mcp_api_keys WHERE id = ?`, item.ID).
		Scan(&prefix, &hash); err != nil {
		t.Fatal(err)
	}
	if prefix == full || string(hash) == full || string(hash) == string(testPepper) {
		t.Fatal("SQLite 不可保存完整 key 或 pepper")
	}
	if len(hash) != 32 || !strings.HasPrefix(prefix, "mcp_live_") {
		t.Fatalf("預期只保存 public prefix 與 HMAC-SHA-256: prefix=%q hashLen=%d", prefix, len(hash))
	}
}

func TestLastActiveProtection(t *testing.T) {
	service, _ := newTestService(t, "")
	item, _, err := service.Create(t.Context(), CreateInput{Name: "only"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Disable(t.Context(), item.ID, item.Version); !errors.Is(err, ErrLastActive) {
		t.Fatalf("停用最後一組 active key 應回 ErrLastActive，實際:%v", err)
	}
	if err := service.Delete(t.Context(), item.ID, item.Version); !errors.Is(err, ErrLastActive) {
		t.Fatalf("刪除最後一組 active key 應回 ErrLastActive，實際:%v", err)
	}
}

func TestOptimisticVersionConflict(t *testing.T) {
	service, _ := newTestService(t, "")
	item, _, err := service.Create(t.Context(), CreateInput{Name: "versioned"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Update(t.Context(), item.ID, UpdateInput{
		Name: "new", Description: "changed", Version: item.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(t.Context(), item.ID, UpdateInput{
		Name: "stale", Version: item.Version,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update 應衝突:%v", err)
	}
	if updated.Version != item.Version+1 {
		t.Fatalf("version 未遞增:%d", updated.Version)
	}
}

func TestConcurrentUpdateHasSingleWinner(t *testing.T) {
	service, _ := newTestService(t, "")
	item, _, err := service.Create(t.Context(), CreateInput{Name: "race"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		wgName := name
		go func() {
			<-start
			_, err := service.Update(context.Background(), item.ID, UpdateInput{
				Name: wgName, Version: item.Version,
			})
			results <- err
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("非預期結果:%v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("應恰有一個成功、一個 conflict: success=%d conflict=%d", successes, conflicts)
	}
}

func TestRepositoryUniquePrefixRollsBack(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Now().UTC()
	generated, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	first := keyRecord{
		APIKey: APIKey{
			ID: generated.ID, Name: "first", MaskedKey: generated.Masked,
			Status: StatusActive, CreatedAt: now, UpdatedAt: now, Version: 1,
		},
		Prefix: generated.Prefix, Hash: keyDigest(testPepper, generated.Full), HashAlgorithm: HashAlgorithm,
	}
	if _, err := repo.Create(t.Context(), first, "created"); err != nil {
		t.Fatal(err)
	}
	secondID, err := randomURLSafe(randomIDBytes)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = secondID
	second.Name = "second"
	if _, err := repo.Create(t.Context(), second, "created"); !errors.Is(err, ErrConflict) {
		t.Fatalf("重複 prefix 應衝突:%v", err)
	}
	items, err := repo.List(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("失敗 transaction 不可留下 partial row:%+v err=%v", items, err)
	}
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatalf("migration 必須可重複執行:%v", err)
	}
}

func TestExpiredAndMalformedKeysAreRejected(t *testing.T) {
	service, _ := newTestService(t, "")
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	item, full, err := service.Create(t.Context(), CreateInput{
		Name: "expiring", ExpiresAt: ptrTime(now.Add(time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(t.Context(), full); !ok {
		t.Fatal("尚未到期應成功")
	}
	service.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, ok := service.Authenticate(t.Context(), full); ok {
		t.Fatal("已到期應拒絕")
	}
	if got, err := service.Get(t.Context(), item.ID); err != nil || got.Status != "expired" {
		t.Fatalf("管理清單應顯示 expired: %+v err=%v", got, err)
	}
	for _, bad := range []string{"", "mcp_live_bad", full + "x", "Bearer " + full} {
		if _, ok := service.Authenticate(t.Context(), bad); ok {
			t.Errorf("畸形 key 不應通過:%q", bad)
		}
	}
}

func TestConcurrentAuthenticateAndReload(t *testing.T) {
	service, _ := newTestService(t, "")
	_, full, err := service.Create(t.Context(), CreateInput{Name: "concurrent"})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 100 {
				if _, ok := service.Authenticate(context.Background(), full); !ok {
					t.Error("並行驗證意外失敗")
					return
				}
			}
		})
	}
	for range 8 {
		wg.Go(func() {
			for range 20 {
				if err := service.Reload(context.Background()); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestReloadFailurePreservesSnapshot(t *testing.T) {
	service, repo := newTestService(t, "")
	_, full, err := service.Create(t.Context(), CreateInput{Name: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Reload(t.Context()); err == nil {
		t.Fatal("關閉資料庫後 reload 應失敗")
	}
	if _, ok := service.Authenticate(t.Context(), full); !ok {
		t.Fatal("reload 失敗時應保留上一份可用快照")
	}
}

func TestLastUsedIsFlushedOnShutdown(t *testing.T) {
	repo := newTestRepository(t)
	service, err := NewService(
		t.Context(), repo, testPepper, "",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	item, full, err := service.Create(t.Context(), CreateInput{Name: "used"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Authenticate(t.Context(), full); !ok {
		t.Fatal("驗證應成功")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.Get(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastUsedAt == nil {
		t.Fatal("graceful shutdown 應 flush last_used_at")
	}
}

func TestContextCancellation(t *testing.T) {
	repo := newTestRepository(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repo.List(ctx); err == nil {
		t.Fatal("已取消 context 應使查詢失敗")
	}
}

func TestPepperMismatchFailsStartup(t *testing.T) {
	repo := newTestRepository(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := NewService(t.Context(), repo, testPepper, "", logger)
	if err != nil {
		t.Fatal(err)
	}
	_ = service.Close()
	if _, err := NewService(t.Context(), repo,
		[]byte("different-pepper-value-that-is-32-bytes"), "", logger); err == nil {
		t.Fatal("不同 pepper 必須明確拒絕啟動")
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
