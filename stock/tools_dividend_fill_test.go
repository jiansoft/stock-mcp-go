// Package stock 測試 get_dividend_fill 的 API client、輸入驗證、摘要與註冊。
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

// fakeFillQuerier 是同時具備基本 Querier 與 DividendFillQuerier 的假資料來源。
type fakeFillQuerier struct {
	fakeQuerier
	envelope *DividendFillEnvelope
	err      error
	gotLimit int
}

func (f *fakeFillQuerier) DividendFill(_ context.Context, _ string, limit int) (*DividendFillEnvelope, error) {
	f.gotLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleFill 是三次除權息的示意資料:最近一次已填息、另外兩次尚未填息。
func sampleFill() *DividendFillEnvelope {
	return &DividendFillEnvelope{
		StockSymbol: "2330",
		DataAsOf:    ptr("2026-10-08"),
		Summary:     DividendFillSummary{Events: 3, Filled: 1, AverageTradingDaysToFill: ptr(2.0), MedianTradingDaysToFill: ptr(2.0)},
		Events: []DividendFillEvent{
			{ExDate: "2026-09-16", CashDividend: ptr(7.0), StockDividend: ptr(0.0), PreExDate: "2026-09-15", PreExClose: ptr(2385.0),
				Filled: true, FillDate: ptr("2026-09-17"), TradingDaysToFill: ptr(2), TradingDaysSinceEx: 16, LatestClose: ptr(2430.0)},
			{ExDate: "2026-06-11", CashDividend: ptr(6.0), StockDividend: ptr(0.0), PreExDate: "2026-06-10", PreExClose: ptr(2255.0),
				TradingDaysSinceEx: 80, LatestClose: ptr(2200.0), GapPercent: ptr(2.44)},
			{ExDate: "2026-03-17", CashDividend: ptr(6.0), StockDividend: ptr(0.0), PreExDate: "2026-03-16", PreExClose: ptr(1845.0),
				TradingDaysSinceEx: 140, LatestClose: ptr(1800.0), GapPercent: ptr(2.44)},
		},
	}
}

// TestAPIClientDividendFill 驗證路徑、query string、404 語意與空陣列補齊。
func TestAPIClientDividendFill(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/2330/dividend-fill":
			gotQuery = r.URL.RawQuery
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":null,"summary":{"events":0,"filled":0,"average_trading_days_to_fill":null,"median_trading_days_to_fill":null},"events":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	fill, err := client.DividendFill(t.Context(), "2330", 5)
	if err != nil || fill.Events == nil || fill.Summary.AverageTradingDaysToFill != nil {
		t.Fatalf("DividendFill() = %#v, %v", fill, err)
	}
	if gotQuery != "limit=5" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
	if _, err := client.DividendFill(t.Context(), "9999", 12); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
}

// TestDividendFillTool 驗證預設筆數、範圍檢查、錯誤分層與摘要內容。
func TestDividendFillTool(t *testing.T) {
	newToolset := func(f *fakeFillQuerier) *dividendFillToolset {
		return &dividendFillToolset{fill: f, logf: func(string, ...any) {}}
	}

	f := &fakeFillQuerier{envelope: sampleFill()}
	result, output, err := newToolset(f).dividendFill(t.Context(), nil, DividendFillInput{Symbol: "2330"})
	if err != nil {
		t.Fatalf("dividendFill 不應失敗:%v", err)
	}
	if f.gotLimit != 12 {
		t.Fatalf("預設應為 12,收到 %d", f.gotLimit)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"股票 2330 最近 3 次除權息:1 次已填息,平均 2 個交易日、中位數 2 個交易日(資料截至 2026-10-08;除權息當日填息算第 1 天)。",
		"最近一次 2026-09-16:現金股利 7 元、股票股利 0 元,除權息前收盤 2385 元,第 2 個交易日(2026-09-17)填息。",
		"尚未填息:\n- 2026-06-11 除權息,已過 80 個交易日,最新收盤 2200 元,距填息價 2255 元還差 2.44%",
		"- 2026-03-17 除權息,已過 140 個交易日",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(DividendFillOutput); !ok || out.DataKind != "dividend_fill" || out.Summary.Filled != 1 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).dividendFill(t.Context(), nil, DividendFillInput{Symbol: "2330", Limit: 40}); err != nil || f.gotLimit != 40 {
		t.Fatalf("limit 40 應原樣傳給 API:%v %d", err, f.gotLimit)
	}
	if _, _, err := newToolset(f).dividendFill(t.Context(), nil, DividendFillInput{Symbol: "2330", Limit: 41}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit 超界應回錯誤:%v", err)
	}
	if _, _, err := newToolset(f).dividendFill(t.Context(), nil, DividendFillInput{Symbol: " "}); err == nil {
		t.Fatal("空白代號應回錯誤")
	}
	if _, _, err := newToolset(&fakeFillQuerier{err: ErrStockNotFound}).dividendFill(t.Context(), nil, DividendFillInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeFillQuerier{err: errors.New("boom")}).dividendFill(t.Context(), nil, DividendFillInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestDividendFillSummaryEdgeCases 涵蓋沒有紀錄、最近一次尚未填息、全部未填息與未填息清單上限。
func TestDividendFillSummaryEdgeCases(t *testing.T) {
	empty := dividendFillSummary(&DividendFillEnvelope{StockSymbol: "6488"})
	if !strings.Contains(empty, "股票 6488 沒有可分析的除權息紀錄。") || !strings.Contains(empty, "免責聲明") {
		t.Fatalf("摘要錯誤:%s", empty)
	}

	e := sampleFill()
	e.Events[0].Filled, e.Events[0].FillDate, e.Events[0].TradingDaysToFill = false, nil, nil
	e.Events = append(e.Events, e.Events[1], e.Events[1])
	e.Summary = DividendFillSummary{Events: 5}
	summary := dividendFillSummary(e)
	if !strings.Contains(summary, "0 次已填息(資料截至") || strings.Contains(summary, "平均") {
		t.Errorf("沒有已填息時不應有平均:%s", summary)
	}
	if !strings.Contains(summary, "除權息前收盤 2385 元,尚未填息。") {
		t.Errorf("最近一次尚未填息的描述錯誤:%s", summary)
	}
	if got := strings.Count(summary, "\n- "); got != maxUnfilledInSummary {
		t.Errorf("未填息清單應最多 %d 筆,實際 %d:%s", maxUnfilledInSummary, got, summary)
	}
}

// TestDividendFillToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestDividendFillToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeFillQuerier{}, nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), t1, nil); err != nil {
		t.Fatalf("server.Connect:%v", err)
	}
	cs, err := client.Connect(t.Context(), t2, nil)
	if err != nil {
		t.Fatalf("client.Connect:%v", err)
	}
	defer cs.Close()
	found := false
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if tool.Name == "get_dividend_fill" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_dividend_fill 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_dividend_fill")
	}
}

// TestDividendFillSummaryStockDividendTarget 驗證配股時註明調整後的填息價,未填息清單也用它;
// fill_target 與前一日收盤相同時不註明。
func TestDividendFillSummaryStockDividendTarget(t *testing.T) {
	e := &DividendFillEnvelope{
		StockSymbol: "2539",
		DataAsOf:    ptr("2026-10-30"),
		Summary:     DividendFillSummary{Events: 1},
		Events: []DividendFillEvent{
			{ExDate: "2026-08-20", CashDividend: ptr(0.6), StockDividend: ptr(2.2), PreExDate: "2026-08-19", PreExClose: ptr(41.25),
				FillTarget: ptr(33.81), TradingDaysSinceEx: 50, LatestClose: ptr(31.35), GapPercent: ptr(7.28)},
		},
	}
	summary := dividendFillSummary(e)
	for _, want := range []string{
		"除權息前收盤 41.25 元(依配股調整的填息價 33.81 元),尚未填息。",
		"最新收盤 31.35 元,距填息價 33.81 元還差 7.28%",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}

	e.Events[0].FillTarget = ptr(41.25)
	if summary := dividendFillSummary(e); strings.Contains(summary, "依配股調整") {
		t.Errorf("填息價等於前一日收盤時不應註明調整:%s", summary)
	}
}
