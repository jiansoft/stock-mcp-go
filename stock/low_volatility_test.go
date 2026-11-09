package stock

import (
	"slices"
	"strings"
	"testing"
)

// TestBacktestLowVolatility 驗證回測的年化波動度與 beta 上限(範圍、query、schema、摘要)以及 rank_by=low_volatility。
func TestBacktestLowVolatility(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MaxVolatilityPercent: ptr(25.0), MaxBeta: ptr(0.8), RankBy: "low_volatility"})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	q := opt.query()
	if q.Get("max_volatility_percent") != "25" || q.Get("max_beta") != "0.8" || q.Get("rank_by") != "low_volatility" {
		t.Fatalf("query = %v", q)
	}
	for _, in := range []ScreenBacktestInput{{MaxVolatilityPercent: ptr(0.5)}, {MaxVolatilityPercent: ptr(201.0)}, {MaxBeta: ptr(-6.0)}, {MaxBeta: ptr(5.5)}} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	schemas := backtestFundamentalSchemas()
	if schemas["max_volatility_percent"] == nil || schemas["max_beta"] == nil {
		t.Fatal("回測 schema 缺少低波動條件")
	}
	if !slices.Contains(screenBacktestSchema().Properties["rank_by"].Enum, any("low_volatility")) {
		t.Fatal("rank_by enum 缺少 low_volatility")
	}
	if rankByText("low_volatility") != "近 240 日年化波動度" || !rankAscending("low_volatility") {
		t.Fatal("低波動應由低到高")
	}
	text := backtestOptionsText(opt)
	for _, want := range []string{"近 240 日年化波動度 ≤ 25%", "beta(對加權指數)≤ 0.8", "取近 240 日年化波動度最低的前"} {
		if !strings.Contains(text, want) {
			t.Fatalf("穩健度摘要缺少 %q:%s", want, text)
		}
	}
	e := sampleScreenBacktest()
	e.MaxVolatilityPercent = ptr(30.0)
	e.MaxBeta = ptr(1.0)
	if text := screenBacktestSummary(e); !strings.Contains(text, "近 240 日年化波動度 ≤ 30%") || !strings.Contains(text, "beta(對加權指數)≤ 1") {
		t.Fatalf("回測摘要缺少低波動條件:%s", text)
	}
	if lowVolatilityTexts(nil, nil) != nil {
		t.Fatal("沒有條件應為 nil")
	}
}

// TestScreenLowVolatility 驗證 screen_stocks 的低波動條件單獨即可篩選、傳給 Data API,以及首筆波動摘要。
func TestScreenLowVolatility(t *testing.T) {
	first := ScreenedStock{StockSymbol: "2412", Name: "中華電", VolatilityPercent: ptr(12.2), Beta: ptr(-0.02)}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxVolatilityPercent: ptr(20.0), SortBy: "volatility"})
	if err != nil || f.gotOpt.MaxVolatilityPercent == nil || f.gotOpt.SortBy != "volatility" {
		t.Fatalf("波動度上限單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.Contains(text, "首筆波動:近 240 日年化波動度 12.2%、beta -0.02(對加權指數)。") {
		t.Fatalf("摘要缺少波動:%s", text)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxBeta: ptr(0.5)}); err != nil {
		t.Fatalf("beta 上限單獨即可篩選:%v", err)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxBeta: ptr(9.0)}); err == nil {
		t.Fatal("beta 超出範圍應失敗")
	}
	if screenVolatilityLine(ScreenedStock{}) != "" {
		t.Fatal("沒有資料時不應輸出")
	}
	enum := screenStocksSchema().Properties["sort_by"].Enum
	if !slices.Contains(enum, any("volatility")) || !slices.Contains(enum, any("beta")) {
		t.Fatal("sort_by enum 缺少 volatility／beta")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MaxVolatilityPercent: ptr(20.0), MaxBeta: ptr(0.5), SortBy: "beta", SortOrder: "asc", Limit: 20})
	for _, want := range []string{"max_volatility_percent=20", "max_beta=0.5", "sort_by=beta"} {
		if !strings.Contains(query, want) {
			t.Fatalf("Data API query 缺少 %s:%s", want, query)
		}
	}
}
