package stock

import (
	"slices"
	"strings"
	"testing"
)

// TestBacktestGraham 驗證回測的本益比 × 股價淨值比上限、流動比率下限(範圍、query、schema、摘要)以及 rank_by=pe_pb_product。
func TestBacktestGraham(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MaxPEPBProduct: ptr(22.5), MinCurrentRatio: ptr(2.0), RankBy: "pe_pb_product"})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	q := opt.query()
	if q.Get("max_pe_pb_product") != "22.5" || q.Get("min_current_ratio") != "2" || q.Get("rank_by") != "pe_pb_product" {
		t.Fatalf("query = %v", q)
	}
	for _, in := range []ScreenBacktestInput{
		{MaxPEPBProduct: ptr(-1.0)}, {MaxPEPBProduct: ptr(1001.0)},
		{MinCurrentRatio: ptr(-1.0)}, {MinCurrentRatio: ptr(101.0)},
	} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	schemas := backtestFundamentalSchemas()
	if schemas["max_pe_pb_product"] == nil || schemas["min_current_ratio"] == nil {
		t.Fatal("回測 schema 缺少葛拉漢條件")
	}
	if !slices.Contains(screenBacktestSchema().Properties["rank_by"].Enum, any("pe_pb_product")) {
		t.Fatal("rank_by enum 缺少 pe_pb_product")
	}
	if rankByText("pe_pb_product") != "本益比 × 股價淨值比" || !rankAscending("pe_pb_product") {
		t.Fatal("葛拉漢乘積應由低到高")
	}
	text := backtestOptionsText(opt)
	for _, want := range []string{"本益比 × 股價淨值比 ≤ 22.5", "流動比率 ≥ 2 倍", "取本益比 × 股價淨值比最低的前"} {
		if !strings.Contains(text, want) {
			t.Fatalf("穩健度摘要缺少 %q:%s", want, text)
		}
	}
	e := sampleScreenBacktest()
	e.MaxPEPBProduct = ptr(20.0)
	e.MinCurrentRatio = ptr(1.5)
	if text := screenBacktestSummary(e); !strings.Contains(text, "本益比 × 股價淨值比 ≤ 20") || !strings.Contains(text, "流動比率 ≥ 1.5 倍") {
		t.Fatalf("回測摘要缺少葛拉漢條件:%s", text)
	}
	if grahamTexts(nil, nil) != nil {
		t.Fatal("沒有條件應為 nil")
	}
}

// TestScreenGraham 驗證 screen_stocks 的葛拉漢條件單獨即可篩選、傳給 Data API、sort_by=pe_pb_product,以及首筆摘要只在相關查詢出現。
func TestScreenGraham(t *testing.T) {
	first := ScreenedStock{StockSymbol: "2002", Name: "中鋼", PEPBProduct: ptr(18.4), CurrentRatio: ptr(1.62)}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxPEPBProduct: ptr(22.5)})
	if err != nil || f.gotOpt.MaxPEPBProduct == nil {
		t.Fatalf("乘積上限單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.Contains(text, "首筆本益比 × 股價淨值比 18.4、流動比率 1.62 倍。") {
		t.Fatalf("摘要缺少葛拉漢指標:%s", text)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinCurrentRatio: ptr(2.0)}); err != nil || f.gotOpt.MinCurrentRatio == nil {
		t.Fatalf("流動比率下限單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{SortBy: "pe_pb_product", SortOrder: "asc"}); err != nil {
		t.Fatalf("依乘積排序單獨即可查詢:%v", err)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinCurrentRatio: ptr(150.0)}); err == nil {
		t.Fatal("流動比率超出範圍應失敗")
	}
	if screenGrahamLine(ScreenStocksInput{MinRDRatioPercent: ptr(5.0)}, first) != "" {
		t.Fatal("沒有葛拉漢條件時不應輸出")
	}
	if screenGrahamLine(ScreenStocksInput{MaxPEPBProduct: ptr(22.5)}, ScreenedStock{}) != "" {
		t.Fatal("沒有資料時不應輸出")
	}
	if !slices.Contains(screenStocksSchema().Properties["sort_by"].Enum, any("pe_pb_product")) {
		t.Fatal("sort_by enum 缺少 pe_pb_product")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MaxPEPBProduct: ptr(22.5), MinCurrentRatio: ptr(2.0), SortBy: "pe_pb_product", SortOrder: "asc", Limit: 20})
	for _, want := range []string{"max_pe_pb_product=22.5", "min_current_ratio=2", "sort_by=pe_pb_product"} {
		if !strings.Contains(query, want) {
			t.Fatalf("Data API query 缺少 %s:%s", want, query)
		}
	}
}
