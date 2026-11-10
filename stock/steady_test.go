package stock

import (
	"strings"
	"testing"
)

// TestBacktestSteady 驗證回測的市值下限與連續獲利年數(範圍、query、schema、摘要)。
func TestBacktestSteady(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinMarketCap: ptr(100.0), MinProfitYears: 5})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	q := opt.query()
	if q.Get("min_market_cap") != "100" || q.Get("min_profit_years") != "5" {
		t.Fatalf("query = %v", q)
	}
	for _, in := range []ScreenBacktestInput{
		{MinMarketCap: ptr(0.0)}, {MinMarketCap: ptr(1000001.0)}, {MinProfitYears: -1}, {MinProfitYears: 31},
	} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	for _, key := range []string{"min_market_cap", "min_profit_years"} {
		if backtestFundamentalSchemas()[key] == nil {
			t.Fatalf("回測 schema 缺少 %s", key)
		}
	}
	text := backtestOptionsText(opt)
	for _, want := range []string{"市值 ≥ 100 億元", "已公布的年度 EPS 連續 5 年為正"} {
		if !strings.Contains(text, want) {
			t.Fatalf("穩健度摘要缺少 %q:%s", want, text)
		}
	}
	e := sampleScreenBacktest()
	e.MinMarketCap = ptr(300.0)
	years := 3
	e.MinProfitYears = &years
	summary := screenBacktestSummary(e)
	for _, want := range []string{"市值 ≥ 300 億元", "已公布的年度 EPS 連續 3 年為正"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("回測摘要缺少 %q:%s", want, summary)
		}
	}
	if steadyTexts(nil, 0) != nil {
		t.Fatal("沒有條件應為 nil")
	}
}

// TestScreenSteady 驗證 screen_stocks 的市值與連續獲利年數單獨即可篩選、傳給 Data API,以及首筆摘要。
func TestScreenSteady(t *testing.T) {
	years := 12
	first := ScreenedStock{StockSymbol: "2330", Name: "台積電", MarketCap: ptr(380000.0), ProfitYears: &years}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinMarketCap: ptr(100.0)})
	if err != nil || f.gotOpt.MinMarketCap == nil {
		t.Fatalf("市值下限單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.Contains(text, "首筆市值 380000 億元、年度 EPS 連續 12 年為正。") {
		t.Fatalf("摘要缺少市值與連續獲利:%s", text)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinProfitYears: 5}); err != nil || f.gotOpt.MinProfitYears != 5 {
		t.Fatalf("連續獲利年數單獨即可篩選:%v %+v", err, f.gotOpt)
	}
	for _, in := range []ScreenStocksInput{{MinMarketCap: ptr(0.5)}, {MinProfitYears: 31}} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	if screenSteadyLine(ScreenedStock{}) != "" {
		t.Fatal("沒有資料時不應輸出")
	}
	for _, key := range []string{"min_market_cap", "min_profit_years"} {
		if screenStocksSchema().Properties[key] == nil {
			t.Fatalf("screen schema 缺少 %s", key)
		}
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MinMarketCap: ptr(100.0), MinProfitYears: 5, Limit: 20})
	for _, want := range []string{"min_market_cap=100", "min_profit_years=5"} {
		if !strings.Contains(query, want) {
			t.Fatalf("Data API query 缺少 %s:%s", want, query)
		}
	}
}
