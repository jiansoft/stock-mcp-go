package stock

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"stockmcp/apikey"
)

// TestStockGoLeasesIssue：成功時回租約並送出會員、端點與 UUID v7 的 request_id；429／403／其他狀態轉成可辨識的 LeaseError。
func TestStockGoLeasesIssue(t *testing.T) {
	var got map[string]any
	var auth string
	status, retry, body := http.StatusCreated, "", `{"lease_id":"l-1","lease_secret":"s-1","expires_at":"2026-11-16T10:00:00Z"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stock/v1/internal/analysis/leases" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	leases, err := NewStockGoLeases(server.URL+"/", "quota-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := leases.IssueLease(t.Context(), 2, "/stocks/screen/backtest")
	if err != nil || lease.ID != "l-1" || lease.Secret != "s-1" {
		t.Fatalf("應取得租約：%+v %v", lease, err)
	}
	id, parseErr := uuid.Parse(got["request_id"].(string))
	if auth != "Bearer quota-secret" || got["member_id"] != float64(2) || got["endpoint"] != "/stocks/screen/backtest" || parseErr != nil || id.Version() != 7 {
		t.Fatalf("請求內容錯誤：%s %+v", auth, got)
	}

	cases := []struct {
		status       int
		retry, body  string
		reason       string
		retryAfter   time.Duration
		messageMatch string
	}{
		{http.StatusTooManyRequests, "3600", `{"error":"quota_exceeded","reason":"daily"}`, "daily", time.Hour, "今日回測次數已用完"},
		{http.StatusTooManyRequests, "30", `{"error":"quota_exceeded","reason":"concurrent"}`, "concurrent", 30 * time.Second, "30 秒"},
		{http.StatusTooManyRequests, "3600", `{"error":"quota_exceeded","reason":"unconfigured"}`, "unconfigured", time.Hour, "尚未設定"},
		{http.StatusForbidden, "", `{"error":"forbidden"}`, "forbidden", 0, "analysis.run"},
		{http.StatusServiceUnavailable, "", `{"error":"busy"}`, "unavailable", 0, "稍後再試"},
		{http.StatusCreated, "", `{}`, "unavailable", 0, "稍後再試"},
	}
	for _, tc := range cases {
		status, retry, body = tc.status, tc.retry, tc.body
		_, err := leases.IssueLease(t.Context(), 2, "/stocks/screen/backtest")
		var leaseErr *LeaseError
		if !errors.As(err, &leaseErr) || leaseErr.Reason != tc.reason || leaseErr.RetryAfter != tc.retryAfter || !strings.Contains(leaseErr.Message(), tc.messageMatch) {
			t.Errorf("%d %s：得到 %v", tc.status, tc.body, err)
		}
	}
	server.Close()
	if _, err := leases.IssueLease(t.Context(), 2, "/stocks/screen/backtest"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("stock_go 無法連線應回 unavailable：%v", err)
	}
	if _, err := NewStockGoLeases("", "x", time.Second); err == nil {
		t.Fatal("缺少位址應回錯誤")
	}
}

// fakeLeaseIssuer 記錄請領並回固定租約或錯誤。
type fakeLeaseIssuer struct {
	member   int64
	endpoint string
	err      error
}

func (f *fakeLeaseIssuer) IssueLease(_ context.Context, memberID int64, endpoint string) (Lease, error) {
	f.member, f.endpoint = memberID, endpoint
	if f.err != nil {
		return Lease{}, f.err
	}
	return Lease{ID: "lease-1", Secret: "secret-1"}, nil
}

// TestBacktestLeaseHeaders：回測請求先請領租約並帶三個標頭；沒有會員或請領失敗即不送出；未設定請領 client 不帶標頭。
func TestBacktestLeaseHeaders(t *testing.T) {
	var headers http.Header
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		headers = r.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	issuer := &fakeLeaseIssuer{}
	client := NewAPIClient(server.URL, "k", time.Second).WithLeases(issuer)
	ctx := context.WithValue(context.Background(), principalContextKey{}, apikey.Principal{MemberID: 2})
	var out map[string]any
	if err := client.getBacktest(ctx, "/api/v1/stocks/screen/backtest/compare?a=1", &out); err != nil {
		t.Fatal(err)
	}
	if issuer.member != 2 || issuer.endpoint != "/stocks/screen/backtest/compare" ||
		headers.Get(headerLeaseID) != "lease-1" || headers.Get(headerLeaseSecret) != "secret-1" || headers.Get(headerLeaseMember) != "2" {
		t.Fatalf("租約標頭錯誤：%+v %+v", issuer, headers)
	}
	if err := client.getBacktest(context.Background(), "/api/v1/stocks/screen/backtest", &out); !errors.As(err, new(*LeaseError)) || calls != 1 {
		t.Fatalf("沒有會員應拒絕且不送出：%v %d", err, calls)
	}
	issuer.err = &LeaseError{Reason: "daily", RetryAfter: time.Hour}
	if err := client.getBacktest(ctx, "/api/v1/stocks/screen/backtest", &out); !errors.As(err, new(*LeaseError)) || calls != 1 {
		t.Fatalf("配額用完應不送出：%v %d", err, calls)
	}
	plain := NewAPIClient(server.URL, "k", time.Second)
	if err := plain.getBacktest(ctx, "/api/v1/stocks/screen/backtest", &out); err != nil || headers.Get(headerLeaseID) != "" {
		t.Fatalf("未設定請領 client 不應帶標頭：%v %+v", err, headers)
	}
}

// TestBacktestToolErrorForLease：租約錯誤與 Rust 全域滿載回可辨識的訊息；stock_go 無法使用時記錄日誌。
func TestBacktestToolErrorForLease(t *testing.T) {
	var logged int
	logf := func(string, ...any) { logged++ }
	if err := backtestToolError(&LeaseError{Reason: "daily", RetryAfter: 90 * time.Minute}, "x", ScreenBacktestOptions{}, logf); !strings.Contains(err.Error(), "今日回測次數已用完") || logged != 0 {
		t.Fatalf("配額用完：%v", err)
	}
	if err := backtestToolError(&LeaseError{Reason: "unavailable"}, "x", ScreenBacktestOptions{}, logf); !strings.Contains(err.Error(), "稍後再試") || logged != 1 {
		t.Fatalf("stock_go 無法使用：%v", err)
	}
	if err := backtestToolError(&apiStatusError{status: http.StatusTooManyRequests}, "x", ScreenBacktestOptions{}, logf); !strings.Contains(err.Error(), "忙碌") {
		t.Fatalf("Rust 滿載：%v", err)
	}
	for _, d := range []time.Duration{0, 20 * time.Second, 15 * time.Minute} {
		if retryText(d) == "" {
			t.Errorf("%v 應有文字", d)
		}
	}
	if (&LeaseError{Reason: "no_member"}).Message() == (&LeaseError{Reason: "forbidden"}).Message() {
		t.Error("不同原因應有不同訊息")
	}
}
