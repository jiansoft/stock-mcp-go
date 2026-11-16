package apikey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"

	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 個人 MCP Token 的遠端驗證（會員系統計畫 6.1）：Token 由 stock_go 簽發與解析，本服務只向 stock_go
// 取快照並在記憶體比對，不連資料庫。

const (
	// RemoteRefreshEvery 是定時刷新快照的間隔；撤銷最慢這麼久後生效。
	RemoteRefreshEvery = 30 * time.Second
	// RemoteOnDemandMinInterval 是遇到未知前綴時按需刷新的最短間隔；新 Token 最慢這麼久後可用。
	RemoteOnDemandMinInterval = 5 * time.Second
	// RemoteStaleAfter 是快照最久可沿用的時間；超過仍無法刷新就拒絕所有個人 Token。
	RemoteStaleAfter = 15 * time.Minute
	// remoteTokenScheme 是個人 Token 的前綴；完整格式為 smcp_<12 個十六進位字元>_<base64url 密鑰>。
	remoteTokenScheme = "smcp_"
	remotePrefixLen   = len(remoteTokenScheme) + 12
	remoteTouchEvery  = 5 * time.Second
	remoteTouchMin    = time.Minute
	remoteHTTPTimeout = 5 * time.Second
	remoteMaxBody     = 8 << 20
)

// RemoteToken 是快照中的一把 Token（與 stock_go 的 SnapshotToken 相同結構）。
type RemoteToken struct {
	ID                 string            `json:"id"`
	KeyPrefix          string            `json:"key_prefix"`
	KeyHash            string            `json:"key_hash"`
	HashAlgorithm      string            `json:"hash_algorithm"`
	MemberID           int64             `json:"member_id"`
	HouseholdID        int64             `json:"household_id"`
	HouseholdMemberIDs []int64           `json:"household_member_ids"`
	Account            string            `json:"account"`
	ExpiresAt          time.Time         `json:"expires_at"`
	Grants             map[string]string `json:"grants"`
}

// LegacyPrincipal 是舊 SQLite 金鑰代表的會員（Eddie）當下的授權；stock_go 在他停用時回 null。
type LegacyPrincipal struct {
	MemberID           int64             `json:"member_id"`
	HouseholdID        int64             `json:"household_id"`
	HouseholdMemberIDs []int64           `json:"household_member_ids"`
	Account            string            `json:"account"`
	Grants             map[string]string `json:"grants"`
}

// RemoteSnapshot 是 GET /stock/v1/internal/mcp-tokens/snapshot 的回應。
type RemoteSnapshot struct {
	GeneratedAt     time.Time        `json:"generated_at"`
	Tokens          []RemoteToken    `json:"tokens"`
	LegacyPrincipal *LegacyPrincipal `json:"legacy_principal"`
}

type remoteState struct {
	byPrefix  map[string]RemoteToken
	hashes    map[string][]byte
	legacy    *LegacyPrincipal
	fetchedAt time.Time
}

// RemoteTokens 向 stock_go 同步 Token 快照並驗證個人 Token。驗證熱路徑只讀不可變的記憶體快照；
// 失敗的刷新保留上一份快照，超過 RemoteStaleAfter 仍無法刷新時拒絕所有個人 Token。
type RemoteTokens struct {
	baseURL string
	key     string
	client  *http.Client
	logger  *slog.Logger
	now     func() time.Time

	state atomic.Pointer[remoteState]

	refreshMu     sync.Mutex
	lastOnDemand  time.Time
	usedMu        sync.Mutex
	pendingUsed   map[string]time.Time
	lastTouched   map[string]time.Time
	stop          chan struct{}
	done          chan struct{}
	closeOnce     sync.Once
	refreshFailed atomic.Bool
}

// NewRemoteTokens 建立遠端 Token 驗證器；baseURL 是 stock_go 的 API 位址（例如 http://127.0.0.1:8000），
// syncToken 是 MCP_SYNC_TOKEN。Start 之前所有 Token 都驗證失敗。
func NewRemoteTokens(baseURL, syncToken string, logger *slog.Logger) (*RemoteTokens, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || syncToken == "" {
		return nil, fmt.Errorf("%w: 遠端 Token 驗證需要 stock_go 位址與 MCP_SYNC_TOKEN", ErrValidation)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RemoteTokens{
		baseURL:     baseURL,
		key:         syncToken,
		client:      &http.Client{Timeout: remoteHTTPTimeout},
		logger:      logger,
		now:         time.Now,
		pendingUsed: map[string]time.Time{},
		lastTouched: map[string]time.Time{},
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}, nil
}

// Start 先載入一次快照（失敗只記錄，之後由定時刷新重試），再啟動定時刷新與使用時間回寫。
func (r *RemoteTokens) Start(ctx context.Context) {
	if err := r.Refresh(ctx); err != nil {
		r.logger.Warn("啟動時無法取得 MCP Token 快照，稍後重試", "error", err)
	}
	go r.loop()
}

// Close 停止背景工作並回寫尚未送出的使用時間。
func (r *RemoteTokens) Close() {
	r.closeOnce.Do(func() {
		close(r.stop)
		<-r.done
	})
}

func (r *RemoteTokens) loop() {
	defer close(r.done)
	refresh := time.NewTicker(RemoteRefreshEvery)
	touch := time.NewTicker(remoteTouchEvery)
	defer refresh.Stop()
	defer touch.Stop()
	for {
		select {
		case <-refresh.C:
			ctx, cancel := context.WithTimeout(context.Background(), remoteHTTPTimeout)
			if err := r.Refresh(ctx); err != nil && !r.refreshFailed.Swap(true) {
				r.logger.Warn("MCP Token 快照刷新失敗，沿用上一份快照", "error", err)
			}
			cancel()
		case <-touch.C:
			r.flushUsed()
		case <-r.stop:
			r.flushUsed()
			return
		}
	}
}

// Refresh 取得一份新快照；失敗時保留上一份。
func (r *RemoteTokens) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/stock/v1/internal/mcp-tokens/snapshot", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.key)
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("取得 Token 快照：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("取得 Token 快照：HTTP %d", resp.StatusCode)
	}
	var snap RemoteSnapshot
	if err := json.NewDecoder(io.LimitReader(resp.Body, remoteMaxBody)).Decode(&snap); err != nil {
		return fmt.Errorf("解析 Token 快照：%w", err)
	}
	r.publish(snap)
	r.refreshFailed.Store(false)
	return nil
}

func (r *RemoteTokens) publish(snap RemoteSnapshot) {
	next := &remoteState{
		byPrefix:  make(map[string]RemoteToken, len(snap.Tokens)),
		hashes:    make(map[string][]byte, len(snap.Tokens)),
		legacy:    snap.LegacyPrincipal,
		fetchedAt: r.now(),
	}
	for _, token := range snap.Tokens {
		hash, err := hex.DecodeString(token.KeyHash)
		if err != nil || len(hash) != sha256.Size || !strings.EqualFold(token.HashAlgorithm, "sha256") {
			continue // 格式不符的列不採用（不能因為半套資料而錯誤授權）
		}
		next.byPrefix[token.KeyPrefix] = token
		next.hashes[token.KeyPrefix] = hash
	}
	r.state.Store(next)
}

// IsRemoteToken 回報 token 是否為個人 Token 格式（smcp_ 開頭）。
func IsRemoteToken(token string) bool {
	return strings.HasPrefix(token, remoteTokenScheme)
}

// Lookup 驗證個人 Token，回傳快照中的那一把。未知前綴時按需刷新（最多每 5 秒一次）後再查一次；
// 快照超過 RemoteStaleAfter 沒有更新、Token 已到期或雜湊不符時回 false。
func (r *RemoteTokens) Lookup(ctx context.Context, token string) (RemoteToken, bool) {
	if !IsRemoteToken(token) || len(token) <= remotePrefixLen || token[remotePrefixLen] != '_' {
		return RemoteToken{}, false
	}
	prefix := token[:remotePrefixLen]
	state := r.state.Load()
	if state == nil || !r.hasPrefix(state, prefix) {
		r.refreshOnDemand(ctx)
		state = r.state.Load()
	}
	if state == nil || r.now().Sub(state.fetchedAt) > RemoteStaleAfter {
		return RemoteToken{}, false
	}
	item, ok := state.byPrefix[prefix]
	if !ok || !r.now().Before(item.ExpiresAt) {
		return RemoteToken{}, false
	}
	sum := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(sum[:], state.hashes[prefix]) != 1 {
		return RemoteToken{}, false
	}
	r.markUsed(item.ID)
	return item, true
}

// Legacy 回傳快照中的 legacy_principal（舊金鑰代表的會員）；快照過期或會員停用時回 nil。
func (r *RemoteTokens) Legacy() *LegacyPrincipal {
	state := r.state.Load()
	if state == nil || r.now().Sub(state.fetchedAt) > RemoteStaleAfter {
		return nil
	}
	return state.legacy
}

func (r *RemoteTokens) hasPrefix(state *remoteState, prefix string) bool {
	_, ok := state.byPrefix[prefix]
	return ok
}

// refreshOnDemand 在未知前綴時同步刷新一次；距上次按需刷新不足 5 秒時略過。
func (r *RemoteTokens) refreshOnDemand(ctx context.Context) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	now := r.now()
	if !r.lastOnDemand.IsZero() && now.Sub(r.lastOnDemand) < RemoteOnDemandMinInterval {
		return
	}
	r.lastOnDemand = now
	ctx, cancel := context.WithTimeout(ctx, remoteHTTPTimeout)
	defer cancel()
	if err := r.Refresh(ctx); err != nil {
		r.logger.Warn("按需刷新 MCP Token 快照失敗", "error", err)
	}
}

func (r *RemoteTokens) markUsed(id string) {
	now := r.now().UTC()
	r.usedMu.Lock()
	defer r.usedMu.Unlock()
	if last := r.lastTouched[id]; !last.IsZero() && now.Sub(last) < remoteTouchMin {
		return
	}
	r.lastTouched[id] = now
	r.pendingUsed[id] = now
}

// flushUsed 批次回寫最後使用時間（POST /stock/v1/internal/mcp-tokens/touch）；失敗時下次再送。
func (r *RemoteTokens) flushUsed() {
	r.usedMu.Lock()
	if len(r.pendingUsed) == 0 {
		r.usedMu.Unlock()
		return
	}
	batch := r.pendingUsed
	r.pendingUsed = map[string]time.Time{}
	r.usedMu.Unlock()

	type use struct {
		ID         string    `json:"id"`
		LastUsedAt time.Time `json:"last_used_at"`
	}
	uses := make([]use, 0, len(batch))
	for id, at := range batch {
		uses = append(uses, use{ID: id, LastUsedAt: at})
	}
	body, _ := json.Marshal(uses)
	ctx, cancel := context.WithTimeout(context.Background(), remoteHTTPTimeout)
	defer cancel()
	err := func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/stock/v1/internal/mcp-tokens/touch", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+r.key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	}()
	if err != nil {
		r.logger.Warn("回寫 MCP Token 使用時間失敗", "count", len(uses), "error", err)
		r.usedMu.Lock()
		for id, at := range batch {
			if previous, ok := r.pendingUsed[id]; !ok || at.After(previous) {
				r.pendingUsed[id] = at
			}
		}
		r.usedMu.Unlock()
	}
}

// Composite 依 Token 格式分流：smcp_ 開頭的個人 Token 由 stock_go 快照驗證，其餘交給本機 SQLite 舊金鑰。
// Remote 為 nil 時只用 SQLite（stock_go 同步未設定）。
//
// SQLite 舊金鑰的過渡（4-05）：Legacy 為 nil（MCP_LEGACY_SQLITE_FALLBACK=false）或過了 LegacyUntil
// （MCP_LEGACY_SQLITE_UNTIL）即不再接受。有 stock_go 快照時，舊金鑰視為 legacy_principal 那位會員（Eddie），
// 授權為 6.1 所列六項權限與 Eddie 當下有效權限的交集；Eddie 停用（legacy_principal 為 null）或快照過期即失效，
// 不能成為繞過權限的後門。沒有設定 stock_go 時（會員系統上線前）沿用改版前的行為。
type Composite struct {
	Remote *RemoteTokens
	Legacy *Service
	// LegacyUntil 非零時，從該時間起不再接受 SQLite 舊金鑰。
	LegacyUntil time.Time
	// now 供測試注入時間；nil 時用 time.Now。
	now func() time.Time
}

// legacyPermissions 是舊金鑰的授權上限：等同勾選 market:read、analysis:run、portfolio:read、portfolio:write、
// money_history:read、inventory:read（規格 6.1）。
var legacyPermissions = []string{"market.view", "analysis.run", "portfolio.view", "portfolio.edit", "money_history.view", "inventory.view"}

// legacyGrants 取 legacyPermissions 與 legacy_principal 授權的交集；其餘權限一律 none。
func legacyGrants(current map[string]string) map[string]string {
	grants := make(map[string]string, len(legacyPermissions))
	for _, permission := range legacyPermissions {
		grants[permission] = "none"
		if scope := current[permission]; scope != "" {
			grants[permission] = scope
		}
	}
	return grants
}

// legacyAllowed 判斷目前是否仍接受 SQLite 舊金鑰。
func (c Composite) legacyAllowed() bool {
	if c.Legacy == nil {
		return false
	}
	if c.LegacyUntil.IsZero() {
		return true
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	return now().Before(c.LegacyUntil)
}

// Authenticate 實作 web.Authenticator。
func (c Composite) Authenticate(ctx context.Context, token string) (Principal, bool) {
	if IsRemoteToken(token) {
		if c.Remote == nil {
			return Principal{}, false
		}
		item, ok := c.Remote.Lookup(ctx, token)
		if !ok {
			return Principal{}, false
		}
		return Principal{KeyID: item.ID, Prefix: item.KeyPrefix, MemberID: item.MemberID, HouseholdID: item.HouseholdID,
			Household: item.HouseholdMemberIDs, Account: item.Account, Grants: item.Grants}, true
	}
	if !c.legacyAllowed() {
		return Principal{}, false
	}
	principal, ok := c.Legacy.Authenticate(ctx, token)
	if !ok {
		return Principal{}, false
	}
	principal.Legacy = true
	if c.Remote == nil {
		return principal, true
	}
	legacy := c.Remote.Legacy()
	if legacy == nil {
		return Principal{}, false
	}
	principal.MemberID = legacy.MemberID
	principal.HouseholdID = legacy.HouseholdID
	principal.Household = legacy.HouseholdMemberIDs
	principal.Account = legacy.Account
	principal.Grants = legacyGrants(legacy.Grants)
	return principal, true
}
