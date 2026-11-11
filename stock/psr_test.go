package stock

import (
	"strings"
	"testing"
)

// TestBacktestFisher 驗證回測的 PSR 上限與近 3 年平均稅後淨利率下限(範圍、query、schema、摘要)。
func TestBacktestFisher(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MaxPSR: ptr(1.5), MinAvgNetMargin3Y: ptr(-5.0)})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	q := opt.query()
	if q.Get("max_psr") != "1.5" || q.Get("min_avg_net_margin_3y") != "-5" {
		t.Fatalf("query = %v", q)
	}
	for _, in := range []ScreenBacktestInput{
		{MaxPSR: ptr(-1.0)}, {MaxPSR: ptr(101.0)},
		{MinAvgNetMargin3Y: ptr(-101.0)}, {MinAvgNetMargin3Y: ptr(101.0)},
	} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	schemas := backtestFundamentalSchemas()
	if schemas["max_psr"] == nil || schemas["min_avg_net_margin_3y"] == nil {
		t.Fatal("回測 schema 缺少費雪條件")
	}
	text := backtestOptionsText(opt)
	for _, want := range []string{"PSR ≤ 1.5 倍", "近 3 年平均稅後淨利率 ≥ -5%"} {
		if !strings.Contains(text, want) {
			t.Fatalf("穩健度摘要缺少 %s:%s", want, text)
		}
	}
	e := sampleScreenBacktest()
	e.MaxPSR = ptr(2.0)
	e.MinAvgNetMargin3Y = ptr(10.0)
	summary := screenBacktestSummary(e)
	for _, want := range []string{"PSR ≤ 2 倍", "近 3 年平均稅後淨利率 ≥ 10%"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("回測摘要缺少 %s:%s", want, summary)
		}
	}
	if fisherTexts(nil, nil) != nil {
		t.Fatal("沒有條件應為 nil")
	}
}

// TestScreenFisher 驗證 screen_stocks 的 PSR 與平均淨利率單獨即可篩選、傳給 Data API,以及首筆摘要只在相關查詢出現。
func TestScreenFisher(t *testing.T) {
	first := ScreenedStock{StockSymbol: "2330", Name: "台積電", PSR: ptr(1.2), AvgNetMargin3YPercent: ptr(12.5)}
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{first}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MaxPSR: ptr(2.0)})
	if err != nil || f.gotOpt.MaxPSR == nil || f.gotOpt.MinAvgNetMargin3Y != nil {
		t.Fatalf("PSR 應傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.Contains(text, "首筆PSR 1.2 倍、近 3 年平均稅後淨利率 12.5%。") {
		t.Fatalf("摘要缺少費雪數值:%s", text)
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinAvgNetMargin3Y: ptr(10.0)}); err != nil {
		t.Fatalf("只設平均淨利率即可篩選:%v", err)
	}
	for _, in := range []ScreenStocksInput{{MaxPSR: ptr(101.0)}, {MinAvgNetMargin3Y: ptr(-101.0)}} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, in); err == nil {
			t.Fatalf("%+v 應失敗", in)
		}
	}
	if screenFisherLine(ScreenStocksInput{MinMarketCap: ptr(100.0)}, first) != "" {
		t.Fatal("沒有費雪條件時不應輸出")
	}
	if screenFisherLine(ScreenStocksInput{MaxPSR: ptr(2.0)}, ScreenedStock{}) != "" {
		t.Fatal("沒有資料時不應輸出")
	}
	schema := screenStocksSchema()
	if schema.Properties["max_psr"] == nil || schema.Properties["min_avg_net_margin_3y"] == nil {
		t.Fatal("screen schema 缺少費雪條件")
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MaxPSR: ptr(1.5), MinAvgNetMargin3Y: ptr(8.0), Limit: 20})
	for _, want := range []string{"max_psr=1.5", "min_avg_net_margin_3y=8"} {
		if !strings.Contains(query, want) {
			t.Fatalf("Data API query 缺少 %s:%s", want, query)
		}
	}
}
