package apikey

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRemoteTokensRefreshFailures：無法建立請求、連不上、非 200、內容不是 JSON 時回錯誤並保留上一份快照。
func TestRemoteTokensRefreshFailures(t *testing.T) {
	fake := &fakeStockGo{}
	remote, clock := newTestRemote(t, fake)
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))}
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	fake.badJSON = true
	if err := remote.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "解析 Token 快照") {
		t.Fatalf("內容不是 JSON 應回解析錯誤：%v", err)
	}
	fake.badJSON = false
	if _, ok := remote.Lookup(context.Background(), tokenA); !ok {
		t.Fatal("刷新失敗時應沿用上一份快照")
	}

	closed := httptest.NewServer(fake.handler())
	closed.Close()
	unreachable, err := NewRemoteTokens(closed.URL, "sync-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := unreachable.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "取得 Token 快照") {
		t.Fatalf("連不上應回錯誤：%v", err)
	}

	invalid, err := NewRemoteTokens("http://bad\x7fhost", "sync-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := invalid.Refresh(context.Background()); err == nil {
		t.Fatal("無法建立請求應回錯誤")
	}
}

// TestRemoteTokensStartFailureAndOnDemandFailure：啟動時取不到快照只記錄（之後重試）；按需刷新失敗時 Token 驗證失敗。
func TestRemoteTokensStartFailureAndOnDemandFailure(t *testing.T) {
	fake := &fakeStockGo{fail: true}
	remote, _ := newTestRemote(t, fake)
	remote.Start(context.Background())
	t.Cleanup(remote.Close)
	if _, ok := remote.Lookup(context.Background(), tokenA); ok {
		t.Fatal("沒有快照時不應通過")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.fetches < 2 {
		t.Fatalf("啟動與按需刷新都應嘗試取得快照，實際 %d 次", fake.fetches)
	}
}

// TestRemoteTokensBackgroundLoop：背景迴圈定時刷新（失敗只記錄一次）並定時回寫使用時間。
func TestRemoteTokensBackgroundLoop(t *testing.T) {
	fake := &fakeStockGo{}
	remote, clock := newTestRemote(t, fake)
	remote.refreshEvery = 20 * time.Millisecond
	remote.touchEvery = 20 * time.Millisecond
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))}
	remote.Start(context.Background())
	t.Cleanup(remote.Close)
	if _, ok := remote.Lookup(context.Background(), tokenA); !ok {
		t.Fatal("Start 後應已載入快照")
	}
	fake.mu.Lock()
	fake.fail = true
	fake.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		touched, fetches := len(fake.touched), fake.fetches
		fake.mu.Unlock()
		if touched >= 1 && fetches >= 3 && remote.refreshFailed.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("背景迴圈應定時刷新並回寫使用時間")
}

// TestRemoteTokensTouchFailureRequeues：回寫失敗（非 200、連不上、無法建立請求）時保留待回寫的使用時間，下次再送。
func TestRemoteTokensTouchFailureRequeues(t *testing.T) {
	fake := &fakeStockGo{touchFail: true}
	remote, clock := newTestRemote(t, fake)
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))}
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	remote.Lookup(context.Background(), tokenA)
	remote.flushUsed()
	if len(remote.pendingUsed) != 1 {
		t.Fatalf("回寫失敗應保留待回寫項目：%v", remote.pendingUsed)
	}
	// 失敗期間又記到更新的使用時間：保留較新的那筆
	newer := clock.Now().Add(time.Hour).UTC()
	remote.pendingUsed["id-a"] = newer
	fake.touchFail = true
	remote.flushUsed()
	if got := remote.pendingUsed["id-a"]; !got.Equal(newer) {
		t.Fatalf("重新排入時應保留較新的時間：%v", got)
	}

	fake.touchFail = false
	remote.flushUsed()
	if len(fake.touched) != 1 || len(remote.pendingUsed) != 0 {
		t.Fatalf("恢復後應送出並清空：%+v %v", fake.touched, remote.pendingUsed)
	}

	closed := httptest.NewServer(fake.handler())
	closed.Close()
	remote.baseURL = closed.URL
	remote.pendingUsed["id-a"] = newer
	remote.flushUsed()
	if len(remote.pendingUsed) != 1 {
		t.Fatal("連不上時應保留待回寫項目")
	}
	remote.baseURL = "http://bad\x7fhost"
	remote.flushUsed()
	if len(remote.pendingUsed) != 1 {
		t.Fatal("無法建立請求時應保留待回寫項目")
	}
}

// TestCompositeRejectsUnknownRemoteToken：遠端快照裡沒有的個人 Token 一律拒絕。
func TestCompositeRejectsUnknownRemoteToken(t *testing.T) {
	fake := &fakeStockGo{}
	remote, clock := newTestRemote(t, fake)
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))}
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := (Composite{Remote: remote}).Authenticate(context.Background(), tokenB); ok {
		t.Fatal("快照裡沒有的個人 Token 不應通過")
	}
}
