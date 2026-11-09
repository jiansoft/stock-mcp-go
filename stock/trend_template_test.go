package stock

import (
	"strings"
	"testing"
)

// TestTrendTemplateFilter 驗證 Minervini 趨勢模板:回測 query、schema 與摘要,以及 screen_stocks 單獨篩選、Data API query 與摘要。
func TestTrendTemplateFilter(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{TrendTemplate: true})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	if got := opt.query().Get("trend_template"); got != "true" {
		t.Fatalf("query = %q,應為 true", got)
	}
	plain, _ := screenBacktestOptions(ScreenBacktestInput{})
	if plain.query().Has("trend_template") {
		t.Fatal("未設定時不應帶入 query")
	}
	if backtestFundamentalSchemas()["trend_template"] == nil {
		t.Fatal("回測 schema 缺少 trend_template")
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, trendTemplateText) {
		t.Fatalf("穩健度摘要缺少趨勢模板:%s", text)
	}
	e := sampleScreenBacktest()
	e.TrendTemplate = true
	if text := screenBacktestSummary(e); !strings.Contains(text, trendTemplateText) {
		t.Fatalf("回測摘要缺少趨勢模板:%s", text)
	}
	if trendTemplateTexts(false) != nil {
		t.Fatal("沒有條件應為 nil")
	}

	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{{StockSymbol: "2330", Name: "台積電", TrendTemplate: ptr(true)}}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{TrendTemplate: true})
	if err != nil || !f.gotOpt.TrendTemplate {
		t.Fatalf("單獨即可篩選且傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.HasPrefix(text, "篩選條件:"+trendTemplateText+"。") {
		t.Fatalf("摘要缺少趨勢模板條件:%s", text)
	}
	query := apiScreenQueryForTest(t, ScreenOptions{TrendTemplate: true, SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
	if !strings.Contains(query, "trend_template=true") {
		t.Fatalf("Data API query 缺少趨勢模板:%s", query)
	}
	if screenStocksSchema().Properties["trend_template"] == nil {
		t.Fatal("screen_stocks schema 缺少 trend_template")
	}
}
