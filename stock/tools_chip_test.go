// Package stock 測試 get_chip_data 的 API client、輸入驗證、摘要與註冊。
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

// fakeChipQuerier 是同時具備基本 Querier 與 ChipQuerier 的假資料來源。
type fakeChipQuerier struct {
	fakeQuerier
	envelope *ChipEnvelope
	err      error
	gotDays  int
}

func (f *fakeChipQuerier) StockChip(_ context.Context, symbol string, days int) (*ChipEnvelope, error) {
	f.gotDays = days
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

func i64(v int64) *int64 { return &v }

func sampleChip() *ChipEnvelope {
	asOf := "2026-10-06"
	return &ChipEnvelope{
		StockSymbol: "2330",
		DataAsOf:    &asOf,
		Daily: []ChipDay{{
			Date: "2026-10-06", ForeignNet: i64(-1_672_231), TrustNet: i64(154_586), DealerNet: i64(395_975), TotalNet: i64(-1_121_670),
			MarginBalance: i64(30_500), MarginChange: i64(365), ShortBalance: i64(49), ShortChange: i64(3),
		}},
		Streak:             ChipStreak{ForeignDays: -3, TrustDays: 2},
		HolderDistribution: []HolderWeek{{Date: "2026-10-02", MajorHolders: 1485, MajorPercent: ptr(84.77), TotalHolders: 3_010_913}},
		Insider:            &InsiderSummary{Month: "2026-08", Insiders: 42, Shares: 1_712_981_294, Pledged: 3_328_000, PledgePercent: ptr(0.19)},
	}
}

// TestAPIClientStockChip 驗證路徑、days 參數、404 語意與空陣列補齊。
func TestAPIClientStockChip(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/2330/chip":
			gotQuery = r.URL.RawQuery
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":null,"daily":null,"streak":{"foreign_days":0,"trust_days":0},"holder_distribution":null,"insider":null,"broker_flow":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	chip, err := client.StockChip(t.Context(), "2330", 5)
	if err != nil || chip.Daily == nil || chip.HolderDistribution == nil || chip.Insider != nil {
		t.Fatalf("StockChip() = %#v, %v", chip, err)
	}
	if gotQuery != "days=5" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
	if _, err := client.StockChip(t.Context(), "9999", 5); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
}

// TestChipTool 驗證預設 days、範圍檢查、錯誤分層與摘要內容。
func TestChipTool(t *testing.T) {
	newToolset := func(f *fakeChipQuerier) *chipToolset {
		return &chipToolset{chips: f, logf: func(string, ...any) {}}
	}

	f := &fakeChipQuerier{envelope: sampleChip()}
	result, output, err := newToolset(f).stockChip(t.Context(), nil, ChipInput{Symbol: "2330"})
	if err != nil {
		t.Fatalf("stockChip 不應失敗:%v", err)
	}
	if f.gotDays != 20 {
		t.Fatalf("days 預設應為 20,收到 %d", f.gotDays)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"外資 -1672 張", "投信 +154 張", "外資連續賣超 3 天", "投信連續買超 2 天", "融資餘額 30500 張(+365 張)", "千張大戶 2026-10-02 持股 84.77%", "董監 2026-08 設質比例 0.19%", "免責聲明"} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(ChipOutput); !ok || out.DataKind != "stock_chip" || out.StockSymbol != "2330" {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).stockChip(t.Context(), nil, ChipInput{Symbol: "2330", Days: 121}); err == nil || !strings.Contains(err.Error(), "days") {
		t.Fatalf("days 超界應回錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeChipQuerier{err: ErrStockNotFound}).stockChip(t.Context(), nil, ChipInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeChipQuerier{err: errors.New("boom")}).stockChip(t.Context(), nil, ChipInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestChipSummaryWithoutData 沒有每日資料時仍給出可讀摘要。
func TestChipSummaryWithoutData(t *testing.T) {
	summary := chipSummary(&ChipEnvelope{StockSymbol: "00679B", Daily: []ChipDay{}, HolderDistribution: []HolderWeek{}})
	if !strings.Contains(summary, "股票 00679B 尚無每日籌碼資料。") {
		t.Fatalf("摘要錯誤:%s", summary)
	}
}

// TestChipToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestChipToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeChipQuerier{}, nil)
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
		if tool.Name == "get_chip_data" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_chip_data 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_chip_data")
	}
}

// TestChipMainFlowText 驗證每日主力的累計與買賣超天數:沒有主力資料的日子不計,都沒有時不出現這句。
func TestChipMainFlowText(t *testing.T) {
	daily := []ChipDay{
		{Date: "2026-10-20", MainNet: i64(1_351), MainShare: ptr(4.08)},
		{Date: "2026-10-17", MainNet: i64(-2_000)},
		{Date: "2026-10-16", MainNet: i64(0)},
		{Date: "2026-10-15"},
	}
	if got, want := mainFlowText(daily), "近 3 個有主力資料的交易日主力累計 -649 張(買超 1 日、賣超 1 日)。"; got != want {
		t.Fatalf("mainFlowText() = %q,want %q", got, want)
	}
	if got := mainFlowText(daily[3:]); got != "" {
		t.Fatalf("沒有主力資料應回空字串:%q", got)
	}
	e := sampleChip()
	if strings.Contains(chipSummary(e), "主力累計") {
		t.Fatal("沒有主力資料的股票不應出現主力累計")
	}
	e.Daily = daily
	if summary := chipSummary(e); !strings.Contains(summary, "主力累計 -649 張") {
		t.Fatalf("摘要缺少主力累計:%s", summary)
	}
}

// TestChipStreakInvariantWithDifferentDays 驗證 B05:連續天數獨立於 days 參數,無論查詢 days=5 或 days=60,streak 皆保持一致。
func TestChipStreakInvariantWithDifferentDays(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":"2026-10-16","daily":[],"streak":{"foreign_days":-8,"trust_days":13},"holder_distribution":[],"insider":null,"broker_flow":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	chip5, err := client.StockChip(t.Context(), "2330", 5)
	if err != nil {
		t.Fatalf("StockChip(days=5) failed: %v", err)
	}
	chip60, err := client.StockChip(t.Context(), "2330", 60)
	if err != nil {
		t.Fatalf("StockChip(days=60) failed: %v", err)
	}

	if chip5.Streak != chip60.Streak {
		t.Fatalf("days=5 與 days=60 之 Streak 不一致: %+v vs %+v", chip5.Streak, chip60.Streak)
	}
	if chip5.Streak.ForeignDays != -8 || chip5.Streak.TrustDays != 13 {
		t.Fatalf("Streak 數值不符預期: %+v", chip5.Streak)
	}
}
