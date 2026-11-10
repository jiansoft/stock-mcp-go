package stock

import (
	"slices"
	"strings"
	"testing"
)

// TestBacktestRDRatio 驗證回測的近四季研發費用率下限(範圍、query、schema、摘要)以及 rank_by=rd_ratio。
func TestBacktestRDRatio(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinRDRatioPercent: ptr(5.0), RankBy: "rd_ratio"})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	q := opt.query()
	if q.Get("min_rd_ratio_percent") != "5" || q.Get("rank_by") != "rd_ratio" {
		t.Fatalf("query = %v", q)
	}
	for _, in := range []ScreenBacktestInput{{MinRDRatioPercent: ptr(-1.0)}, {MinRDRatioPercent: ptr(101.0)}} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	if backtestFundamentalSchemas()["min_rd_ratio_percent"] == nil {
		t.Fatal("回測 schema 缺少 min_rd_ratio_percent")
	}
	if !slices.Contains(screenBacktestSchema().Properties["rank_by"].Enum, any("rd_ratio")) {
		t.Fatal("rank_by enum 缺少 rd_ratio")
	}
	if rankByText("rd_ratio") != "近四季研發費用率" || rankAscending("rd_ratio") {
		t.Fatal("研發費用率應由高到低")
	}
	text := backtestOptionsText(opt)
	for _, want := range []string{"近四季研發費用率 ≥ 5%", "取近四季研發費用率最高的前"} {
		if !strings.Contains(text, want) {
			t.Fatalf("穩健度摘要缺少 %q:%s", want, text)
		}
	}
	e := sampleScreenBacktest()
	e.MinRDRatioPercent = ptr(8.0)
	if text := screenBacktestSummary(e); !strings.Contains(text, "近四季研發費用率 ≥ 8%") {
		t.Fatalf("回測摘要缺少研發費用率條件:%s", text)
	}
	if rdRatioTexts(nil) != nil {
		t.Fatal("沒有條件應為 nil")
	}
}

// TestScreenRDRatio 驗證 screen_stocks 的研發費用率下限單獨即可篩選、傳給 Data API、sort_by=rd_ratio,以及首筆摘要。
func TestScreenRDRatio(t *testing.T) {
	first := ScreenedStock{StockSymbol: "2454", Name: "聯發科", RDRatioPercent: ptr(25.8)}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinRDRatioPercent: ptr(5.0)})
	if err != nil || f.gotOpt.MinRDRatioPercent == nil {
		t.Fatalf("研發費用率下限單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.Contains(text, "首筆近四季研發費用率 25.8%。") {
		t.Fatalf("摘要缺少研發費用率:%s", text)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinRDRatioPercent: ptr(150.0)}); err == nil {
		t.Fatal("研發費用率超出範圍應失敗")
	}
	if screenRDRatioLine(ScreenedStock{}) != "" {
		t.Fatal("沒有資料時不應輸出")
	}
	if !slices.Contains(screenStocksSchema().Properties["sort_by"].Enum, any("rd_ratio")) {
		t.Fatal("sort_by enum 缺少 rd_ratio")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MinRDRatioPercent: ptr(5.0), SortBy: "rd_ratio", SortOrder: "desc", Limit: 20})
	for _, want := range []string{"min_rd_ratio_percent=5", "sort_by=rd_ratio"} {
		if !strings.Contains(query, want) {
			t.Fatalf("Data API query 缺少 %s:%s", want, query)
		}
	}
}
