package stock

import (
	"slices"
	"strings"
	"testing"
)

// TestBacktestBias240 驗證回測的年線乖離率上限(範圍、query、schema、摘要)以及 rank_by=bias240。
func TestBacktestBias240(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MaxBias240: ptr(-15.0), RankBy: "bias240"})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	q := opt.query()
	if q.Get("max_bias240") != "-15" || q.Get("rank_by") != "bias240" {
		t.Fatalf("query = %v", q)
	}
	for _, in := range []ScreenBacktestInput{{MaxBias240: ptr(-91.0)}, {MaxBias240: ptr(101.0)}} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	if backtestFundamentalSchemas()["max_bias240"] == nil {
		t.Fatal("回測 schema 缺少 max_bias240")
	}
	if !slices.Contains(screenBacktestSchema().Properties["rank_by"].Enum, any("bias240")) {
		t.Fatal("rank_by enum 缺少 bias240")
	}
	if rankByText("bias240") != "年線乖離率" || !rankAscending("bias240") {
		t.Fatal("年線乖離應由低到高")
	}
	text := backtestOptionsText(opt)
	for _, want := range []string{"年線乖離率 ≤ -15%", "取年線乖離率最低的前"} {
		if !strings.Contains(text, want) {
			t.Fatalf("穩健度摘要缺少 %q:%s", want, text)
		}
	}
	e := sampleScreenBacktest()
	e.MaxBias240 = ptr(-20.0)
	if text := screenBacktestSummary(e); !strings.Contains(text, "年線乖離率 ≤ -20%") {
		t.Fatalf("回測摘要缺少年線乖離條件:%s", text)
	}
	if bias240Texts(nil) != nil {
		t.Fatal("沒有條件應為 nil")
	}
}

// TestScreenBias240 驗證 screen_stocks 的年線乖離上限與 sort_by=bias240 單獨即可篩選、傳給 Data API,以及首筆摘要。
func TestScreenBias240(t *testing.T) {
	first := ScreenedStock{StockSymbol: "2330", Name: "台積電", Bias240Percent: ptr(-18.5)}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxBias240: ptr(-15.0)})
	if err != nil || f.gotOpt.MaxBias240 == nil {
		t.Fatalf("乖離上限單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.Contains(text, "首筆年線乖離:收盤相對 240 日均線 -18.5%。") {
		t.Fatalf("摘要缺少年線乖離:%s", text)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{SortBy: "bias240"}); err != nil {
		t.Fatalf("依年線乖離排序單獨即可查詢:%v", err)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxBias240: ptr(150.0)}); err == nil {
		t.Fatal("乖離超出範圍應失敗")
	}
	if screenBias240Line(ScreenedStock{}) != "" {
		t.Fatal("沒有資料時不應輸出")
	}
	if !slices.Contains(screenStocksSchema().Properties["sort_by"].Enum, any("bias240")) {
		t.Fatal("sort_by enum 缺少 bias240")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MaxBias240: ptr(-15.0), SortBy: "bias240", SortOrder: "asc", Limit: 20})
	for _, want := range []string{"max_bias240=-15", "sort_by=bias240"} {
		if !strings.Contains(query, want) {
			t.Fatalf("Data API query 缺少 %s:%s", want, query)
		}
	}
}
