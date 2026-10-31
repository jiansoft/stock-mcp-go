package stock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeDividendFillStatsQuerier 記錄收到的 years 並回傳預設資料或錯誤。
type fakeDividendFillStatsQuerier struct {
	got      int
	envelope *DividendFillStatsEnvelope
	err      error
}

func (f *fakeDividendFillStatsQuerier) DividendFillStats(_ context.Context, years int) (*DividendFillStatsEnvelope, error) {
	f.got = years
	return f.envelope, f.err
}

func sampleDividendFillStats() *DividendFillStatsEnvelope {
	return &DividendFillStatsEnvelope{
		Years: 5, From: "2021-10-31", To: "2026-10-31", MatureTradingDays: 120,
		Buckets: []DividendFillBucket{
			{Bucket: "below_3", Label: "<3%", Events: 3600, MatureEvents: 3476, FillRate20d: ptr(71.4), FillRate60d: ptr(83.1), FillRate120d: ptr(87.9), MedianTradingDaysToFill: ptr(4.0)},
			{Bucket: "at_least_7", Label: "≥7%", Events: 3},
		},
	}
}

// TestAPIClientDividendFillStats 驗證路徑、years 與空陣列補齊。
func TestAPIClientDividendFillStats(t *testing.T) {
	var gotURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"years":3,"from":"2023-10-31","to":"2026-10-31","mature_trading_days":120,"buckets":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)
	stats, err := client.DividendFillStats(t.Context(), 3)
	if err != nil || stats.Buckets == nil || stats.Years != 3 {
		t.Fatalf("DividendFillStats() = %#v, %v", stats, err)
	}
	if gotURL != "/api/v1/market/dividend-fill-stats?years=3" {
		t.Fatalf("網址錯誤:%q", gotURL)
	}
	if _, err := client.DividendFillStats(t.Context(), 0); err != nil || gotURL != "/api/v1/market/dividend-fill-stats" {
		t.Fatalf("未指定 years 不帶參數:%q %v", gotURL, err)
	}
}

// TestDividendFillStatsTool 驗證範圍檢查、錯誤分層與摘要內容(沒有可統計事件的組另外說明)。
func TestDividendFillStatsTool(t *testing.T) {
	f := &fakeDividendFillStatsQuerier{envelope: sampleDividendFillStats()}
	ts := &dividendFillStatsToolset{sq: f, logf: func(string, ...any) {}}
	result, output, err := ts.dividendFillStats(t.Context(), nil, DividendFillStatsInput{Years: 8})
	if err != nil || f.got != 8 {
		t.Fatalf("dividendFillStats 不應失敗:%v %d", err, f.got)
	}
	if out, ok := output.(DividendFillStatsOutput); !ok || out.DataKind != "dividend_fill_stats" || len(out.Buckets) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"全市場填息基準(除權息日 2021-10-31 之後～2026-10-31,近 5 年;",
		"- 現金殖利率 <3%(3476 次):20 日內填息 71.4%、60 日內 83.1%、120 日內 87.9%,已填息中位數 4 個交易日。",
		"- 現金殖利率 ≥7%:沒有可統計的事件。",
		"免責聲明",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	for _, bad := range []int{-1, 9} {
		if _, _, err := ts.dividendFillStats(t.Context(), nil, DividendFillStatsInput{Years: bad}); err == nil || !strings.Contains(err.Error(), "years") {
			t.Errorf("years=%d 應回錯誤:%v", bad, err)
		}
	}
	failing := &dividendFillStatsToolset{sq: &fakeDividendFillStatsQuerier{err: errors.New("boom")}, logf: func(string, ...any) {}}
	if _, _, err := failing.dividendFillStats(t.Context(), nil, DividendFillStatsInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestHoldingFillPeerText 驗證自身沒有已填息紀錄時才附同組中位數。
func TestHoldingFillPeerText(t *testing.T) {
	peer := &DividendFillBucket{Label: "3～5%", MedianTradingDaysToFill: ptr(13.0)}
	item := HoldingDividendFillItem{History: DividendFillSummary{Events: 2}, Peer: peer}
	if got := fillPeerText(item); got != ";同殖利率組(3～5%)全市場中位數 13 個交易日" {
		t.Fatalf("沒有自身紀錄應附同組中位數:%q", got)
	}
	item.History.AverageTradingDaysToFill = ptr(8.0)
	if got := fillPeerText(item); got != "" {
		t.Fatalf("有自身紀錄不附:%q", got)
	}
	if got := fillPeerText(HoldingDividendFillItem{}); got != "" {
		t.Fatalf("沒有基準不附:%q", got)
	}
}
