package stock

import (
	"strings"
	"testing"
)

// TestRevenueAccelerationFilter 驗證營收加速月數:回測 query、範圍、schema 與摘要,以及 screen_stocks 單獨篩選與 Data API query。
func TestRevenueAccelerationFilter(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinRevenueAccelerationMonths: 2})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	if got := opt.query().Get("min_revenue_acceleration_months"); got != "2" {
		t.Fatalf("query = %q,應為 2", got)
	}
	plain, _ := screenBacktestOptions(ScreenBacktestInput{})
	if plain.query().Has("min_revenue_acceleration_months") {
		t.Fatal("未設定時不應帶入 query")
	}
	for _, bad := range []ScreenBacktestInput{{MinRevenueAccelerationMonths: 12}, {MinRevenueAccelerationMonths: -1}} {
		if _, err := screenBacktestOptions(bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	if backtestFundamentalSchemas()["min_revenue_acceleration_months"] == nil {
		t.Fatal("回測 schema 缺少 min_revenue_acceleration_months")
	}
	want := "已公布的月營收年增率連續 2 個月逐月提高"
	if text := backtestOptionsText(opt); !strings.Contains(text, want) {
		t.Fatalf("穩健度摘要缺少營收加速:%s", text)
	}
	e := sampleScreenBacktest()
	e.MinRevenueAccelerationMonths = ptr(2)
	if text := screenBacktestSummary(e); !strings.Contains(text, want) {
		t.Fatalf("回測摘要缺少營收加速:%s", text)
	}
	if revenueAccelerationTexts(0) != nil {
		t.Fatal("沒有條件應為 nil")
	}

	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{{StockSymbol: "2330", Name: "台積電"}}}}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinRevenueAccelerationMonths: 3}); err != nil ||
		f.gotOpt.MinRevenueAccelerationMonths != 3 {
		t.Fatalf("單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	for _, bad := range []ScreenStocksInput{{MinRevenueAccelerationMonths: 12}, {MinRevenueAccelerationMonths: -1}} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MinRevenueAccelerationMonths: 3, SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
	if !strings.Contains(query, "min_revenue_acceleration_months=3") {
		t.Fatalf("Data API query 缺少營收加速:%s", query)
	}
	if screenStocksSchema().Properties["min_revenue_acceleration_months"] == nil {
		t.Fatal("screen_stocks schema 缺少 min_revenue_acceleration_months")
	}
}
