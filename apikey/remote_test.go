package apikey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeStockGo 模擬 stock_go 的 Token 快照與回寫端點。
type fakeStockGo struct {
	mu        sync.Mutex
	tokens    []RemoteToken
	legacy    *LegacyPrincipal
	fail      bool
	badJSON   bool
	touchFail bool
	fetches   int
	touched   []map[string]any
	authSeen  string
}

func (f *fakeStockGo) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stock/v1/internal/mcp-tokens/snapshot", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.fetches++
		f.authSeen = r.Header.Get("Authorization")
		if f.fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if f.badJSON {
			_, _ = w.Write([]byte("{not json"))
			return
		}
		_ = json.NewEncoder(w).Encode(RemoteSnapshot{Tokens: f.tokens, LegacyPrincipal: f.legacy})
	})
	mux.HandleFunc("POST /stock/v1/internal/mcp-tokens/touch", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var uses []map[string]any
		_ = json.Unmarshal(body, &uses)
		f.mu.Lock()
		if f.touchFail {
			f.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.touched = append(f.touched, uses...)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"updated":1}`))
	})
	return mux
}

func remoteToken(id, token string, expires time.Time) RemoteToken {
	sum := sha256.Sum256([]byte(token))
	return RemoteToken{ID: id, KeyPrefix: token[:remotePrefixLen], KeyHash: hex.EncodeToString(sum[:]), HashAlgorithm: "sha256",
		MemberID: 2, HouseholdID: 1, HouseholdMemberIDs: []int64{1, 2}, Account: "unice", ExpiresAt: expires,
		Grants: map[string]string{"portfolio.view": "own"}}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestRemote(t *testing.T, fake *fakeStockGo) (*RemoteTokens, *testClock) {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	remote, err := NewRemoteTokens(server.URL+"/", "sync-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)}
	remote.now = clock.Now
	return remote, clock
}

const (
	tokenA = "smcp_0123456789ab_secretAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	tokenB = "smcp_ba9876543210_secretBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

// TestRemoteTokensLoadAndVerify：快照載入後以完整字串的 SHA-256 比對；密鑰錯、格式錯、到期都拒絕；帶同步金鑰。
func TestRemoteTokensLoadAndVerify(t *testing.T) {
	fake := &fakeStockGo{}
	remote, clock := newTestRemote(t, fake)
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))}
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.authSeen != "Bearer sync-secret" {
		t.Fatalf("快照請求應帶 MCP_SYNC_TOKEN，得到 %q", fake.authSeen)
	}
	item, ok := remote.Lookup(context.Background(), tokenA)
	if !ok || item.ID != "id-a" || item.MemberID != 2 || item.Grants["portfolio.view"] != "own" {
		t.Fatalf("合法 Token 應通過：%+v %v", item, ok)
	}
	for _, bad := range []string{tokenA + "x", tokenA[:len(tokenA)-1] + "Z", "smcp_short", "mcp_live_xxx", ""} {
		if _, ok := remote.Lookup(context.Background(), bad); ok {
			t.Errorf("%q 不應通過", bad)
		}
	}
	clock.Add(2 * time.Hour)
	fake.tokens[0].ExpiresAt = clock.Now().Add(time.Hour) // 快照內容仍是舊的到期時間，直到刷新
	if _, ok := remote.Lookup(context.Background(), tokenA); ok {
		t.Fatal("到期的 Token 不應通過")
	}
}

// TestRemoteTokensOnDemandRefresh：未知前綴按需刷新（新 Token 不必等 30 秒），但最多每 5 秒一次。
func TestRemoteTokensOnDemandRefresh(t *testing.T) {
	fake := &fakeStockGo{}
	remote, clock := newTestRemote(t, fake)
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))}
	fake.mu.Unlock()
	if _, ok := remote.Lookup(context.Background(), tokenA); !ok {
		t.Fatal("未知前綴應按需刷新後通過")
	}
	before := fake.fetches
	if _, ok := remote.Lookup(context.Background(), tokenB); ok {
		t.Fatal("不存在的 Token 不應通過")
	}
	if fake.fetches != before {
		t.Fatalf("5 秒內不應再按需刷新，實際多了 %d 次", fake.fetches-before)
	}
	clock.Add(6 * time.Second)
	remote.Lookup(context.Background(), tokenB)
	if fake.fetches != before+1 {
		t.Fatalf("超過 5 秒後應再按需刷新一次，實際多了 %d 次", fake.fetches-before)
	}
}

// TestRemoteTokensRevokeAndStale：撤銷後刷新即失效；stock_go 失聯時沿用快照，超過 15 分鐘拒絕所有個人 Token。
func TestRemoteTokensRevokeAndStale(t *testing.T) {
	fake := &fakeStockGo{legacy: &LegacyPrincipal{MemberID: 1}}
	remote, clock := newTestRemote(t, fake)
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(24*time.Hour)), remoteToken("id-b", tokenB, clock.Now().Add(24*time.Hour))}
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.tokens = fake.tokens[1:] // 撤銷 A
	fake.mu.Unlock()
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := remote.Lookup(context.Background(), tokenA); ok {
		t.Fatal("撤銷後應失效")
	}

	fake.mu.Lock()
	fake.fail = true
	fake.mu.Unlock()
	clock.Add(14 * time.Minute)
	if err := remote.Refresh(context.Background()); err == nil {
		t.Fatal("stock_go 失聯時刷新應回錯誤")
	}
	if _, ok := remote.Lookup(context.Background(), tokenB); !ok || remote.Legacy() == nil {
		t.Fatal("失聯 15 分鐘內應沿用最後一份快照")
	}
	clock.Add(2 * time.Minute)
	if _, ok := remote.Lookup(context.Background(), tokenB); ok || remote.Legacy() != nil {
		t.Fatal("失聯超過 15 分鐘應拒絕所有個人 Token")
	}
}

// TestRemoteTokensTouch：通過驗證的 Token 批次回寫最後使用時間，同一把一分鐘內只記一次。
func TestRemoteTokensTouch(t *testing.T) {
	fake := &fakeStockGo{}
	remote, clock := newTestRemote(t, fake)
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))}
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		remote.Lookup(context.Background(), tokenA)
	}
	remote.flushUsed()
	if len(fake.touched) != 1 || fake.touched[0]["id"] != "id-a" {
		t.Fatalf("應回寫一筆使用時間：%+v", fake.touched)
	}
	remote.flushUsed()
	if len(fake.touched) != 1 {
		t.Fatal("沒有新的使用時不應再回寫")
	}
}

// TestCompositeRoutesByTokenFormat：smcp_ 開頭走遠端；遠端未設定時個人 Token 一律拒絕；其他格式走 SQLite。
func TestCompositeRoutesByTokenFormat(t *testing.T) {
	fake := &fakeStockGo{}
	remote, clock := newTestRemote(t, fake)
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))}
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p, ok := (Composite{Remote: remote}).Authenticate(context.Background(), tokenA); !ok || p.KeyID != "id-a" {
		t.Fatalf("個人 Token 應由遠端驗證：%+v %v", p, ok)
	}
	if _, ok := (Composite{}).Authenticate(context.Background(), tokenA); ok {
		t.Fatal("遠端未設定時個人 Token 應被拒")
	}
	if _, ok := (Composite{Remote: remote}).Authenticate(context.Background(), "mcp_live_abc_def"); ok {
		t.Fatal("沒有 SQLite 服務時舊格式應被拒")
	}
	if _, err := NewRemoteTokens("", "x", nil); err == nil {
		t.Fatal("缺少 stock_go 位址應回錯誤")
	}
}

// TestRemoteTokensStartAndClose：Start 先載入快照並啟動背景工作；Close 停止時回寫尚未送出的使用時間。
func TestRemoteTokensStartAndClose(t *testing.T) {
	fake := &fakeStockGo{}
	remote, clock := newTestRemote(t, fake)
	fake.tokens = []RemoteToken{remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))}
	remote.Start(context.Background())
	if _, ok := remote.Lookup(context.Background(), tokenA); !ok {
		t.Fatal("Start 後應已載入快照")
	}
	remote.Close()
	remote.Close() // 可重複呼叫
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.touched) != 1 {
		t.Fatalf("Close 時應回寫使用時間：%+v", fake.touched)
	}
}

// TestRemoteTokensRejectsMalformedSnapshotRows：雜湊格式或演算法不符的列不採用。
func TestRemoteTokensRejectsMalformedSnapshotRows(t *testing.T) {
	fake := &fakeStockGo{}
	remote, clock := newTestRemote(t, fake)
	bad := remoteToken("id-a", tokenA, clock.Now().Add(time.Hour))
	bad.HashAlgorithm = "md5"
	broken := remoteToken("id-b", tokenB, clock.Now().Add(time.Hour))
	broken.KeyHash = "zz"
	fake.tokens = []RemoteToken{bad, broken}
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{tokenA, tokenB} {
		if _, ok := remote.Lookup(context.Background(), token); ok {
			t.Errorf("格式不符的快照列不應通過：%s", token[:remotePrefixLen])
		}
	}
}
