package stock

import (
	"slices"
	"strings"
	"testing"
)

// TestBacktestPEG 驗證回測 max_peg:範圍、query、schema、摘要,以及 rank_by=peg 的白名單、文字與方向。
func TestBacktestPEG(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MaxPEG: ptr(1.0), RankBy: "peg"})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	if q := opt.query(); q.Get("max_peg") != "1" || q.Get("rank_by") != "peg" {
		t.Fatalf("query = %v,應帶 max_peg=1 與 rank_by=peg", q)
	}
	for _, bad := range []float64{0, 10.5} {
		if _, err := screenBacktestOptions(ScreenBacktestInput{MaxPEG: ptr(bad)}); err == nil || !strings.Contains(err.Error(), "max_peg") {
			t.Fatalf("max_peg=%v 應失敗:%v", bad, err)
		}
	}
	if backtestFundamentalSchemas()["max_peg"] == nil {
		t.Fatal("回測 schema 缺少 max_peg")
	}
	if !slices.Contains(screenBacktestSchema().Properties["rank_by"].Enum, any("peg")) {
		t.Fatal("rank_by enum 缺少 peg")
	}
	if rankByText("peg") != "PEG" || !rankAscending("peg") {
		t.Fatal("PEG 應由低到高")
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, "PEG(本益比 ÷ 近四季 EPS 成長率)≤ 1") || !strings.Contains(text, "取PEG最低的前") {
		t.Fatalf("穩健度摘要缺少 PEG:%s", text)
	}
	e := sampleScreenBacktest()
	e.MaxPEG = ptr(0.8)
	if text := screenBacktestSummary(e); !strings.Contains(text, "PEG(本益比 ÷ 近四季 EPS 成長率)≤ 0.8") {
		t.Fatalf("回測摘要缺少 PEG:%s", text)
	}
	if pegTexts(nil) != nil {
		t.Fatal("沒有條件應為 nil")
	}
}

// TestScreenPEG 驗證 screen_stocks 的 max_peg 單獨即可篩選、sort_by=peg 單獨即可查,以及首筆 PEG 摘要。
func TestScreenPEG(t *testing.T) {
	first := ScreenedStock{StockSymbol: "2330", Name: "台積電", PERatio: ptr(24.75), EPSTTMGrowthPercent: ptr(53.22), PEGRatio: ptr(0.47)}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxPEG: ptr(1.0)})
	if err != nil || f.gotOpt.MaxPEG == nil || *f.gotOpt.MaxPEG != 1 {
		t.Fatalf("max_peg 單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.Contains(text, "首筆 PEG:0.47(本益比 24.75 ÷ 近四季 EPS 成長率 53.22%;虧損或衰退時無)。") {
		t.Fatalf("摘要缺少 PEG:%s", text)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{SortBy: "peg"}); err != nil {
		t.Fatalf("只設 peg 排序就應能查詢:%v", err)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxPEG: ptr(0.0)}); err == nil {
		t.Fatal("max_peg=0 應失敗")
	}
	if screenPEGLine(ScreenedStock{}) != "" {
		t.Fatal("沒有 PEG 時不應輸出")
	}
	if screenStocksSchema().Properties["max_peg"] == nil || !slices.Contains(screenStocksSchema().Properties["sort_by"].Enum, any("peg")) {
		t.Fatal("screen_stocks schema 缺少 max_peg 或 peg 排序")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MaxPEG: ptr(1.5), SortBy: "peg", SortOrder: "asc", Limit: 20})
	if !strings.Contains(query, "max_peg=1.5") || !strings.Contains(query, "sort_by=peg") {
		t.Fatalf("Data API query 缺少 PEG:%s", query)
	}
}
