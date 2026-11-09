package stock

import (
	"slices"
	"strings"
	"testing"
)

// TestMagicFormulaRankBy 驗證回測依神奇公式排序:白名單、schema enum、排序文字與方向,以及偏移摘要的「最低」。
func TestMagicFormulaRankBy(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{RankBy: "magic_formula"})
	if err != nil {
		t.Fatalf("magic_formula 應為合法排序:%v", err)
	}
	if got := opt.query().Get("rank_by"); got != "magic_formula" {
		t.Fatalf("query rank_by = %q,應為 magic_formula", got)
	}
	if _, err := screenBacktestOptions(ScreenBacktestInput{RankBy: "magic"}); err == nil || !strings.Contains(err.Error(), "magic_formula") {
		t.Fatalf("不合法的排序應列出 magic_formula:%v", err)
	}
	enum := screenBacktestSchema().Properties["rank_by"].Enum
	if !slices.Contains(enum, any("magic_formula")) || len(enum) != len(backtestRankBy) {
		t.Fatalf("rank_by enum 應含 magic_formula 且與白名單一致:%v", enum)
	}
	if rankByText("magic_formula") != "神奇公式名次合計" || !rankAscending("magic_formula") || rankOrderText("magic_formula") != "由低到高" {
		t.Fatal("神奇公式名次合計應由低到高")
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, "取神奇公式名次合計最低的前") {
		t.Fatalf("偏移摘要應寫名次合計最低:%s", text)
	}
}

// TestScreenMagicFormula 驗證選股 sort_by=magic_formula 單獨即可查、會傳給 Data API,以及首筆神奇公式摘要附排名母體、
// 分子分母與極高資本報酬率提示。
func TestScreenMagicFormula(t *testing.T) {
	first := ScreenedStock{StockSymbol: "6139", Name: "亞翔", EarningsYieldPercent: ptr(15.66), ReturnOnCapitalPercent: ptr(1753.73),
		MagicFormulaRank: ptr(28), MagicFormulaRankedCount: ptr(636), OperatingIncomeTTM: ptr(5e6), InvestedCapital: ptr(285e3), EnterpriseValue: ptr(3.2e7)}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{SortBy: "magic_formula", SortOrder: "asc"})
	if err != nil || f.gotOpt.SortBy != "magic_formula" {
		t.Fatalf("只設 magic_formula 排序就應能查詢並傳給 client:%v %+v", err, f.gotOpt)
	}
	text := resultText(t, result)
	for _, want := range []string{
		"首筆神奇公式:盈餘殖利率 15.66%、資本報酬率 1753.73%、名次合計 28(越小越好,在本次篩選後 636 檔有資料的股票中排名",
		"近四季營業利益 " + thousandsToHundredMillion(ptr(5e6)) + "、投入資本 " + thousandsToHundredMillion(ptr(285e3)),
		"資本報酬率極高",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("摘要缺少 %q:%s", want, text)
		}
	}
	normal := ScreenedStock{EarningsYieldPercent: ptr(33.0), ReturnOnCapitalPercent: ptr(199.5)}
	if line := screenMagicFormulaLine(normal); strings.Contains(line, "資本報酬率極高") {
		t.Fatalf("資本報酬率 200%% 以下不應提示:%s", line)
	}
	if screenMagicFormulaLine(ScreenedStock{StockSymbol: "2881"}) != "" {
		t.Fatal("金融業沒有資料時不應輸出")
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{SortBy: "eps"}); err == nil {
		t.Fatal("其他排序單獨使用仍需篩選條件")
	}
	if !slices.Contains(screenStocksSchema().Properties["sort_by"].Enum, any("magic_formula")) {
		t.Fatal("sort_by enum 缺少 magic_formula")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{SortBy: "magic_formula", SortOrder: "asc", Limit: 20})
	if !strings.Contains(query, "sort_by=magic_formula") {
		t.Fatalf("Data API query 缺少 sort_by:%s", query)
	}
}
