// Package stock 測試選股的近期區間突破條件(range_breakout_days)。
package stock

import (
	"strings"
	"testing"
)

// TestScreenRangeBreakoutDays 驗證區間突破條件單獨即可篩選、傳給 client、送進 Data API query、範圍檢查與摘要說明。
func TestScreenRangeBreakoutDays(t *testing.T) {
	date, description := "2026-10-29", "收盤 10.3 帶量突破 30 日整理區間高點 10"
	fixture := &Screening{Stocks: []ScreenedStock{{
		StockSymbol: "2330", Name: "台積電", RangeBreakoutDate: &date, RangeBreakoutDescription: &description,
	}}}
	f := &fakeStockScreener{result: fixture}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{RangeBreakoutDays: 5})
	if err != nil {
		t.Fatalf("單獨區間突破條件不應失敗:%v", err)
	}
	if f.gotOpt.RangeBreakoutDays != 5 {
		t.Fatalf("區間突破條件未傳給 client:%+v", f.gotOpt)
	}
	if text := resultText(t, result); !strings.HasPrefix(text, "區間突破條件:近 5 個交易日突破且還沒跌回;首筆最近一次突破 2026-10-29(收盤 10.3") {
		t.Fatalf("摘要缺少區間突破說明:%s", text)
	}
	for _, days := range []int{-1, 21} {
		if _, _, err := newScreenToolset(&fakeStockScreener{result: fixture}).screenStocks(t.Context(), nil,
			ScreenStocksInput{RangeBreakoutDays: days}); err == nil {
			t.Errorf("range_breakout_days=%d 應回錯誤", days)
		}
	}
	query := apiScreenQueryForTest(t, ScreenOptions{RangeBreakoutDays: 3, SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
	if !strings.Contains(query, "range_breakout_days=3") {
		t.Fatalf("Data API query 缺少區間突破條件:%s", query)
	}
	query = apiScreenQueryForTest(t, ScreenOptions{MaxPE: ptr(10.0), SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
	if strings.Contains(query, "range_breakout_days") {
		t.Fatalf("未設定時不應帶入:%s", query)
	}
	if screenStocksSchema().Properties["range_breakout_days"] == nil {
		t.Fatal("screen_stocks schema 缺少 range_breakout_days")
	}
	if screenBreakoutLine(0, fixture.Stocks[0]) != "" || screenBreakoutLine(5, ScreenedStock{}) != "" {
		t.Fatal("沒設條件或首筆沒有突破日時不應加說明")
	}
}
