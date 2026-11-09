package stock

import (
	"strings"
	"testing"
)

// TestChipConcentrationFilters 驗證籌碼集中條件(400 張大戶變化、融資變化):回測 query、範圍、schema 與摘要,
// 以及 screen_stocks 單獨篩選與 Data API query。
func TestChipConcentrationFilters(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinHolder400Change: ptr(0.5), MajorChangeWeeks: 2, MaxMarginChangePercent: ptr(-5.0)})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	q := opt.query()
	if q.Get("min_holder_400_change") != "0.5" || q.Get("max_margin_change_percent") != "-5" || q.Get("major_change_weeks") != "2" {
		t.Fatalf("query 不正確:%s", q.Encode())
	}
	plain, _ := screenBacktestOptions(ScreenBacktestInput{})
	if plain.query().Has("min_holder_400_change") || plain.query().Has("max_margin_change_percent") {
		t.Fatal("未設定時不應帶入 query")
	}
	for _, bad := range []ScreenBacktestInput{
		{MinHolder400Change: ptr(100.5)}, {MaxMarginChangePercent: ptr(-101.0)}, {MaxMarginChangePercent: ptr(1000.5)},
	} {
		if _, err := screenBacktestOptions(bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	for _, key := range []string{"min_holder_400_change", "max_margin_change_percent"} {
		if backtestFundamentalSchemas()[key] == nil {
			t.Fatalf("回測 schema 缺少 %s", key)
		}
		if screenStocksSchema().Properties[key] == nil {
			t.Fatalf("screen_stocks schema 缺少 %s", key)
		}
	}
	wantHolder := "400 張大戶持股比例近 2 週變化 ≥ 0.5 個百分點"
	wantMargin := "融資餘額近 20 筆變化 ≤ -5%"
	if text := backtestOptionsText(opt); !strings.Contains(text, wantHolder) || !strings.Contains(text, wantMargin) {
		t.Fatalf("穩健度摘要缺少籌碼集中:%s", text)
	}
	e := sampleScreenBacktest()
	e.MinHolder400Change = ptr(0.5)
	e.MajorChangeWeeks = ptr(2)
	e.MaxMarginChangePercent = ptr(-5.0)
	if text := screenBacktestSummary(e); !strings.Contains(text, wantHolder) || !strings.Contains(text, wantMargin) {
		t.Fatalf("回測摘要缺少籌碼集中:%s", text)
	}
	if got := chipConcentrationTexts(ptr(1.0), 0, nil); len(got) != 1 || !strings.Contains(got[0], "近 4 週") {
		t.Fatalf("週數 0 應為預設 4 週:%v", got)
	}
	if chipConcentrationTexts(nil, 0, nil) != nil {
		t.Fatal("沒有條件應為 nil")
	}

	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{{StockSymbol: "2330", Name: "台積電"}}}}
	for _, in := range []ScreenStocksInput{{MinHolder400Change: ptr(1.0)}, {MaxMarginChangePercent: ptr(-10.0)}} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, in); err != nil {
			t.Fatalf("%+v 單獨即可篩選:%v", in, err)
		}
	}
	if f.gotOpt.MaxMarginChangePercent == nil || *f.gotOpt.MaxMarginChangePercent != -10 {
		t.Fatalf("融資條件未傳給 client:%+v", f.gotOpt)
	}
	for _, bad := range []ScreenStocksInput{{MinHolder400Change: ptr(-100.5)}, {MaxMarginChangePercent: ptr(1001.0)}} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	query := apiScreenQueryForTest(t, ScreenOptions{
		MinHolder400Change: ptr(1.0), MaxMarginChangePercent: ptr(-10.0), SortBy: "stock_symbol", SortOrder: "asc", Limit: 20,
	})
	if !strings.Contains(query, "min_holder_400_change=1") || !strings.Contains(query, "max_margin_change_percent=-10") {
		t.Fatalf("Data API query 缺少籌碼集中:%s", query)
	}
}
