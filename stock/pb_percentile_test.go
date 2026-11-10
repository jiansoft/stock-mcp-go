package stock

import (
	"slices"
	"strings"
	"testing"
)

// TestBacktestPBPercentile 驗證回測的股價淨值比歷史分位上限(範圍、query、schema、摘要)以及 rank_by=pb_percentile。
func TestBacktestPBPercentile(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MaxPBPercentile: ptr(20.0), RankBy: "pb_percentile"})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	q := opt.query()
	if q.Get("max_pb_percentile") != "20" || q.Get("rank_by") != "pb_percentile" {
		t.Fatalf("query = %v", q)
	}
	for _, in := range []ScreenBacktestInput{{MaxPBPercentile: ptr(-1.0)}, {MaxPBPercentile: ptr(101.0)}} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	if backtestFundamentalSchemas()["max_pb_percentile"] == nil {
		t.Fatal("回測 schema 缺少 max_pb_percentile")
	}
	if !slices.Contains(screenBacktestSchema().Properties["rank_by"].Enum, any("pb_percentile")) {
		t.Fatal("rank_by enum 缺少 pb_percentile")
	}
	if rankByText("pb_percentile") != "股價淨值比近 5 年百分位" || !rankAscending("pb_percentile") {
		t.Fatal("淨值比分位應由低到高")
	}
	text := backtestOptionsText(opt)
	for _, want := range []string{"股價淨值比位於近 5 年第 20 百分位以下", "取股價淨值比近 5 年百分位最低的前"} {
		if !strings.Contains(text, want) {
			t.Fatalf("穩健度摘要缺少 %q:%s", want, text)
		}
	}
	e := sampleScreenBacktest()
	e.MaxPBPercentile = ptr(10.0)
	if text := screenBacktestSummary(e); !strings.Contains(text, "股價淨值比位於近 5 年第 10 百分位以下") {
		t.Fatalf("回測摘要缺少淨值比分位條件:%s", text)
	}
	if pbPercentileTexts(nil) != nil {
		t.Fatal("沒有條件應為 nil")
	}
}

// TestScreenPBPercentile 驗證 screen_stocks 的淨值比分位上限與 sort_by=pb_percentile 單獨即可篩選、傳給 Data API,以及首筆摘要。
func TestScreenPBPercentile(t *testing.T) {
	first := ScreenedStock{StockSymbol: "2412", Name: "中華電", PBPercentile: ptr(12.5)}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxPBPercentile: ptr(20.0)})
	if err != nil || f.gotOpt.MaxPBPercentile == nil {
		t.Fatalf("分位上限單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.Contains(text, "首筆股價淨值比位於近 5 年第 12.5 百分位(0 為 5 年最低)。") {
		t.Fatalf("摘要缺少淨值比分位:%s", text)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{SortBy: "pb_percentile"}); err != nil {
		t.Fatalf("依淨值比分位排序單獨即可查詢:%v", err)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxPBPercentile: ptr(150.0)}); err == nil {
		t.Fatal("分位超出範圍應失敗")
	}
	if screenPBPercentileLine(ScreenedStock{}) != "" {
		t.Fatal("沒有資料時不應輸出")
	}
	if !slices.Contains(screenStocksSchema().Properties["sort_by"].Enum, any("pb_percentile")) {
		t.Fatal("sort_by enum 缺少 pb_percentile")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MaxPBPercentile: ptr(20.0), SortBy: "pb_percentile", SortOrder: "asc", Limit: 20})
	for _, want := range []string{"max_pb_percentile=20", "sort_by=pb_percentile"} {
		if !strings.Contains(query, want) {
			t.Fatalf("Data API query 缺少 %s:%s", want, query)
		}
	}
}
