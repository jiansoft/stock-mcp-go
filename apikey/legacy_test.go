package apikey

import (
	"context"
	"testing"
	"time"
)

const legacySQLiteKey = "old-production-key-that-is-not-new-format"

// TestCompositeLegacyIntersectsLegacyPrincipal（4-05 驗收）：舊金鑰視為 legacy_principal 那位會員，授權為六項上限
// 與其當下權限的交集；降權跟著縮小，停用（legacy_principal 為 null）或快照過期即失效。
func TestCompositeLegacyIntersectsLegacyPrincipal(t *testing.T) {
	service, _ := newTestService(t, legacySQLiteKey)
	fake := &fakeStockGo{legacy: &LegacyPrincipal{MemberID: 1, HouseholdID: 1, HouseholdMemberIDs: []int64{1, 2}, Account: "eddie",
		Grants: map[string]string{"market.view": "granted", "analysis.run": "granted", "portfolio.view": "household",
			"portfolio.edit": "household", "money_history.view": "household", "inventory.view": "household", "iam.manage": "granted"}}}
	remote, clock := newTestRemote(t, fake)
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	composite := Composite{Remote: remote, Legacy: service}

	p, ok := composite.Authenticate(context.Background(), legacySQLiteKey)
	if !ok || !p.Legacy || p.MemberID != 1 || p.HouseholdID != 1 || len(p.Household) != 2 || p.Account != "eddie" {
		t.Fatalf("舊金鑰應視為 Eddie：%+v %v", p, ok)
	}
	if p.Grants["portfolio.edit"] != "household" || p.Grants["analysis.run"] != "granted" || p.Grants["iam.manage"] != "" {
		t.Fatalf("授權應只含六項上限：%+v", p.Grants)
	}

	// 移出 root、只剩本人範圍與市場權限：舊金鑰跟著縮小
	fake.mu.Lock()
	fake.legacy.Grants = map[string]string{"market.view": "granted", "portfolio.view": "own", "analysis.run": "none"}
	fake.mu.Unlock()
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, ok = composite.Authenticate(context.Background(), legacySQLiteKey)
	if !ok || p.Grants["portfolio.view"] != "own" || p.Grants["analysis.run"] != "none" || p.Grants["portfolio.edit"] != "none" {
		t.Fatalf("降權後應跟著縮小：%+v", p.Grants)
	}

	// 停用：legacy_principal 為 null
	fake.mu.Lock()
	fake.legacy = nil
	fake.mu.Unlock()
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := composite.Authenticate(context.Background(), legacySQLiteKey); ok {
		t.Fatal("Eddie 停用後舊金鑰應失效")
	}

	// 快照過期
	fake.mu.Lock()
	fake.legacy = &LegacyPrincipal{MemberID: 1, HouseholdID: 1, Grants: map[string]string{"market.view": "granted"}}
	fake.mu.Unlock()
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Add(RemoteStaleAfter + time.Second)
	if _, ok := composite.Authenticate(context.Background(), legacySQLiteKey); ok {
		t.Fatal("快照過期時舊金鑰應失效")
	}
	if _, ok := composite.Authenticate(context.Background(), "wrong-key"); ok {
		t.Fatal("錯誤的金鑰不應通過")
	}
}

// TestCompositeLegacySwitchAndCutoff：關閉開關（Legacy 為 nil）或過了截止時間即不再接受；沒有 stock_go 時沿用改版前行為。
func TestCompositeLegacySwitchAndCutoff(t *testing.T) {
	service, _ := newTestService(t, legacySQLiteKey)
	cutoff := time.Date(2026, 12, 1, 0, 0, 0, 0, time.FixedZone("Asia/Taipei", 8*60*60))
	now := cutoff.Add(-time.Minute)
	composite := Composite{Legacy: service, LegacyUntil: cutoff, now: func() time.Time { return now }}

	p, ok := composite.Authenticate(context.Background(), legacySQLiteKey)
	if !ok || !p.Legacy || p.Grants != nil {
		t.Fatalf("沒有 stock_go 時應沿用改版前行為：%+v %v", p, ok)
	}
	now = cutoff
	if _, ok := composite.Authenticate(context.Background(), legacySQLiteKey); ok {
		t.Fatal("截止時間之後應停用")
	}
	if _, ok := (Composite{}).Authenticate(context.Background(), legacySQLiteKey); ok {
		t.Fatal("關閉開關後應停用")
	}
	if _, ok := (Composite{Legacy: service}).Authenticate(context.Background(), legacySQLiteKey); !ok {
		t.Fatal("沒有截止日時應可使用")
	}
}

// TestLegacyGrants：未列出的權限一律 none；交集不擴權。
func TestLegacyGrants(t *testing.T) {
	grants := legacyGrants(map[string]string{"market.view": "granted", "iam.manage": "granted"})
	if len(grants) != len(legacyPermissions) || grants["market.view"] != "granted" || grants["portfolio.view"] != "none" {
		t.Fatalf("交集錯誤：%+v", grants)
	}
	if _, ok := grants["iam.manage"]; ok {
		t.Fatal("上限以外的權限不應出現")
	}
}
