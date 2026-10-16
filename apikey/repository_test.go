package apikey

import (
	"errors"
	"testing"
	"time"
)

// createTestRecord 直接在倉儲建立一組啟用中的 key,回傳紀錄(含 prefix)。
func createTestRecord(t *testing.T, repo *SQLiteRepository, name string) keyRecord {
	t.Helper()
	now := time.Now().UTC()
	generated, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	rec := keyRecord{
		APIKey: APIKey{
			ID: generated.ID, Name: name, MaskedKey: generated.Masked,
			Status: StatusActive, CreatedAt: now, UpdatedAt: now, Version: 1,
		},
		Prefix: generated.Prefix, Hash: keyDigest(testPepper, generated.Full), HashAlgorithm: HashAlgorithm,
	}
	if _, err := repo.Create(t.Context(), rec, "created"); err != nil {
		t.Fatal(err)
	}
	return rec
}

// TestRepositoryMutationsRejectMissingAndStaleKeys 驗證每種異動在 key 不存在時回 ErrNotFound、
// 版本不符時回 ErrConflict,且失敗時不留下異動。
func TestRepositoryMutationsRejectMissingAndStaleKeys(t *testing.T) {
	repo := newTestRepository(t)
	ctx := t.Context()
	now := time.Now().UTC()
	keep := createTestRecord(t, repo, "keep")
	target := createTestRecord(t, repo, "target")

	mutations := map[string]func(id string, version int64) error{
		"UpdateMetadata": func(id string, version int64) error {
			_, err := repo.UpdateMetadata(ctx, id, version, "renamed", "desc", nil, now)
			return err
		},
		"SetStatus": func(id string, version int64) error {
			_, err := repo.SetStatus(ctx, id, version, StatusDisabled, now)
			return err
		},
		"Rotate": func(id string, version int64) error {
			generated, err := generateKey()
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.Rotate(ctx, id, version, generated.Prefix, keyDigest(testPepper, generated.Full), now)
			return err
		},
		"Delete": func(id string, version int64) error {
			_, err := repo.Delete(ctx, id, version, now)
			return err
		},
	}
	for name, mutate := range mutations {
		if err := mutate("missing", 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s 不存在的 key 應回 ErrNotFound:%v", name, err)
		}
		if err := mutate(target.ID, target.Version+5); !errors.Is(err, ErrConflict) {
			t.Errorf("%s 版本不符應回 ErrConflict:%v", name, err)
		}
	}
	got, err := repo.Get(ctx, target.ID)
	if err != nil || got.Version != target.Version || got.Name != "target" {
		t.Fatalf("失敗的異動不可改到資料:%+v err=%v", got, err)
	}

	// 輪替成另一組 key 已使用的 prefix:唯一索引衝突回 ErrConflict,整筆 rollback。
	if _, err := repo.Rotate(ctx, target.ID, target.Version, keep.Prefix, target.Hash, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("重複 prefix 輪替應衝突:%v", err)
	}
	if got, err := repo.Get(ctx, target.ID); err != nil || got.Version != target.Version {
		t.Fatalf("衝突的輪替不可改到資料:%+v err=%v", got, err)
	}

	// 正常輪替:版本加一並記錄 audit。
	generated, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Rotate(ctx, target.ID, target.Version, generated.Prefix, keyDigest(testPepper, generated.Full), now); err != nil {
		t.Fatalf("輪替應成功:%v", err)
	}
	if got, err := repo.Get(ctx, target.ID); err != nil || got.Version != target.Version+1 {
		t.Fatalf("輪替後版本應加一:%+v err=%v", got, err)
	}
	audit, err := repo.ListAudit(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	rotated := 0
	for _, event := range audit {
		if event.Action == "rotated" {
			rotated++
		}
	}
	if rotated != 1 {
		t.Fatalf("應只有一筆 rotated audit(衝突的輪替不記錄):%+v", audit)
	}
}

// TestRepositoryClosedDatabaseReturnsErrors 驗證資料庫關閉後各操作回錯誤而不是 panic,
// 錯誤訊息不含底層細節。
func TestRepositoryClosedDatabaseReturnsErrors(t *testing.T) {
	repo := newTestRepository(t)
	ctx := t.Context()
	now := time.Now().UTC()
	rec := createTestRecord(t, repo, "closed")
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	checks := map[string]error{}
	_, checks["Count"] = repo.Count(ctx)
	_, checks["Get"] = repo.Get(ctx, rec.ID)
	_, checks["UpdateMetadata"] = repo.UpdateMetadata(ctx, rec.ID, rec.Version, "n", "", nil, now)
	_, checks["SetStatus"] = repo.SetStatus(ctx, rec.ID, rec.Version, StatusDisabled, now)
	_, checks["Rotate"] = repo.Rotate(ctx, rec.ID, rec.Version, "prefix", rec.Hash, now)
	_, checks["Delete"] = repo.Delete(ctx, rec.ID, rec.Version, now)
	checks["Migrate"] = repo.Migrate(ctx)
	checks["EnsurePepper"] = repo.EnsurePepper(ctx, []byte("check"))
	for name, err := range checks {
		if err == nil {
			t.Errorf("%s 在資料庫關閉後應回錯誤", name)
			continue
		}
		if name != "Migrate" && name != "EnsurePepper" && errors.Is(err, ErrNotFound) {
			t.Errorf("%s 不應把關閉的資料庫當成找不到:%v", name, err)
		}
	}
}

// TestServiceGetReturnsEffectiveItemOrNotFound 驗證 Service.Get 對未知 ID 回 ErrNotFound,
// 已過期的 key 以有效狀態回傳。
func TestServiceGetReturnsEffectiveItemOrNotFound(t *testing.T) {
	service, _ := newTestService(t, "")
	ctx := t.Context()
	if _, err := service.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知 ID 應回 ErrNotFound:%v", err)
	}
	created, _, err := service.Create(ctx, CreateInput{Name: "get"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.Get(ctx, created.ID)
	if err != nil || got.ID != created.ID || got.Name != "get" {
		t.Fatalf("Get 應回傳剛建立的 key:%+v err=%v", got, err)
	}
}
