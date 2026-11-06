// Package stock 測試 CANSLIM:screen_canslim 工具、screen_stocks 與回測的 C／A／N 條件。
package stock

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRegime 是只回傳固定趨勢狀態的 MarketRegimeQuerier。
type fakeRegime struct {
	envelope *MarketRegimeEnvelope
	err      error
}

func (f fakeRegime) MarketRegime(context.Context, string) (*MarketRegimeEnvelope, error) {
	return f.envelope, f.err
}

// regimeAt 建立收盤 close、60 日均線 ma60 的趨勢狀態。
func regimeAt(close, ma60 float64) *MarketRegimeEnvelope {
	above := close > ma60
	return &MarketRegimeEnvelope{
		DataAsOf: "2026-11-06", Close: close, Alignment: "mixed",
		MovingAverages: []MovingAverageStatus{{Days: 20, Value: ptr(close)}, {Days: 60, Value: ptr(ma60), Above: &above}},
	}
}

func newCanslimToolset(screener *fakeStockScreener, regime fakeRegime) *canslimToolset {
	return &canslimToolset{screener: screener, regime: regime, logf: func(string, ...any) {}}
}

// TestScreenCanslimDefaults 驗證沒有提供門檻時套用 C、N、L 的歐尼爾預設值(A 預設不篩)、依相對強度降冪排序,
// 附大盤在 60 日均線之上的判斷與台股回測結論。
func TestScreenCanslimDefaults(t *testing.T) {
	yoy, growth, year, high := 80.5, 31.2, 2025, "2026-11-03"
	screener := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{{
		StockSymbol: "2330", Name: "台積電", EPSYoYPercent: &yoy, AnnualEPSGrowthPercent: &growth, AnnualEPSYear: &year,
		NewHighDate: &high, RSScore: ptr(95),
	}}}}
	result, structured, err := newCanslimToolset(screener, fakeRegime{envelope: regimeAt(23000, 22000)}).
		screenCanslim(t.Context(), nil, ScreenCanslimInput{})
	if err != nil {
		t.Fatalf("預設條件不應失敗:%v", err)
	}
	opt := screener.gotOpt
	if *opt.MinEPSYoYPercent != 25 || opt.MinAnnualEPSGrowthPercent != nil || opt.MinROEPercent != nil ||
		opt.NewHighDays != 60 || *opt.MinRSScore != 80 || opt.SortBy != "rs_score" || opt.SortOrder != "desc" {
		t.Fatalf("預設門檻錯誤:%+v", opt)
	}
	output := structured.(CanslimOutput)
	if len(output.Criteria) != 3 || strings.Join(output.Skipped, ",") != "A" || output.Market.Above == nil || !*output.Market.Above || len(output.Stocks) != 1 {
		t.Fatalf("輸出錯誤:%+v", output)
	}
	text := resultText(t, result)
	for _, want := range []string{
		"CANSLIM 條件:C 單季 EPS 年增 ≥ 25%、N 近 60 天創 52 週新高、L 相對強度 ≥ 80(未篩:A)",
		"M 大盤方向(只供參考):加權指數 2026-11-06 收盤 23000.00 在 60 日均線 22000.00 之上",
		"preset=CANSLIM",
		"符合 1 檔(依 rs_score 由高到低",
		"2330 台積電(單季 EPS 年增 80.5%、近 3 年最低年增 31.2%",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
}

// TestScreenCanslimOverridesAndSkip 驗證門檻覆蓋、skip_letters 拿掉 A 與 N,以及 S、I 的選填條件。
func TestScreenCanslimOverridesAndSkip(t *testing.T) {
	screener := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{}}}
	result, structured, err := newCanslimToolset(screener, fakeRegime{envelope: regimeAt(20000, 21000)}).
		screenCanslim(t.Context(), nil, ScreenCanslimInput{
			MinEPSYoYPercent: ptr(40.0), MinRSScore: 90, SkipLetters: []string{" a", "N", "A"},
			MinAvgTradeValue: ptr(50), MinForeignStreak: ptr(3), MinTrustStreak: ptr(2), SortBy: "eps_yoy",
		})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	opt := screener.gotOpt
	if *opt.MinEPSYoYPercent != 40 || opt.MinAnnualEPSGrowthPercent != nil || opt.MinROEPercent != nil ||
		opt.NewHighDays != 0 || *opt.MinRSScore != 90 || opt.SortBy != "eps_yoy" || *opt.MinAvgTradeValue != 50 {
		t.Fatalf("覆蓋或跳過錯誤:%+v", opt)
	}
	output := structured.(CanslimOutput)
	if strings.Join(output.Skipped, ",") != "A,N" {
		t.Fatalf("跳過的字母應去重複並轉大寫:%v", output.Skipped)
	}
	text := resultText(t, result)
	for _, want := range []string{"(未篩:A、N)", "S 近 20 日平均成交 ≥ 50 百萬元", "I 外資連買 ≥ 3 天", "I 投信連買 ≥ 2 天", "跌破 60 日均線", "報酬反而變差", "沒有符合的股票"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
}

// TestScreenCanslimAnnualGrowth 驗證 A 的三種開法:include_annual_growth 用預設門檻、給 A 的門檻就套用,
// 但 skip_letters 含 A 時連明確的門檻也忽略。
func TestScreenCanslimAnnualGrowth(t *testing.T) {
	for _, tc := range []struct {
		name        string
		in          ScreenCanslimInput
		growth, roe *float64
		skipped     string
	}{
		{"include", ScreenCanslimInput{IncludeAnnualGrowth: true}, ptr(25.0), ptr(17.0), ""},
		{"threshold", ScreenCanslimInput{MinAnnualEPSGrowthPercent: ptr(10.0)}, ptr(10.0), ptr(17.0), ""},
		{"roe only", ScreenCanslimInput{MinROEPercent: ptr(12.0)}, ptr(25.0), ptr(12.0), ""},
		{"skip wins", ScreenCanslimInput{MinAnnualEPSGrowthPercent: ptr(10.0), SkipLetters: []string{"A"}}, nil, nil, "A"},
	} {
		screener := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{}}}
		_, structured, err := newCanslimToolset(screener, fakeRegime{envelope: regimeAt(1, 1)}).screenCanslim(t.Context(), nil, tc.in)
		if err != nil {
			t.Fatalf("%s:不應失敗:%v", tc.name, err)
		}
		opt := screener.gotOpt
		same := func(got, want *float64) bool { return (got == nil) == (want == nil) && (got == nil || *got == *want) }
		if !same(opt.MinAnnualEPSGrowthPercent, tc.growth) || !same(opt.MinROEPercent, tc.roe) {
			t.Errorf("%s:A 門檻錯誤:%v %v", tc.name, opt.MinAnnualEPSGrowthPercent, opt.MinROEPercent)
		}
		if got := strings.Join(structured.(CanslimOutput).Skipped, ","); got != tc.skipped {
			t.Errorf("%s:skipped = %q", tc.name, got)
		}
	}
	if screenCanslimSchema().Properties["include_annual_growth"] == nil {
		t.Fatal("schema 缺少 include_annual_growth")
	}
}

// TestScreenCanslimErrors 驗證錯誤的字母、選股失敗與大盤查詢失敗(仍回選股結果、M 標成無法判斷)。
func TestScreenCanslimErrors(t *testing.T) {
	ok := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{}}}
	if _, _, err := newCanslimToolset(ok, fakeRegime{}).screenCanslim(t.Context(), nil,
		ScreenCanslimInput{SkipLetters: []string{"M"}}); err == nil {
		t.Fatal("skip_letters 只能是 C、A、N、L")
	}
	if _, _, err := newCanslimToolset(ok, fakeRegime{}).screenCanslim(t.Context(), nil,
		ScreenCanslimInput{NewHighDays: 400}); err == nil {
		t.Fatal("new_high_days 超出範圍應回錯誤")
	}
	failing := &fakeStockScreener{err: errors.New("boom")}
	if _, _, err := newCanslimToolset(failing, fakeRegime{}).screenCanslim(t.Context(), nil, ScreenCanslimInput{}); err == nil ||
		strings.Contains(err.Error(), "boom") {
		t.Fatalf("選股失敗應回通用錯誤:%v", err)
	}
	result, structured, err := newCanslimToolset(ok, fakeRegime{err: errors.New("down")}).
		screenCanslim(t.Context(), nil, ScreenCanslimInput{})
	if err != nil {
		t.Fatalf("大盤查詢失敗不應擋掉選股:%v", err)
	}
	if structured.(CanslimOutput).Market.Above != nil {
		t.Fatal("大盤查詢失敗時 above_ma60 應為 null")
	}
	if text := resultText(t, result); !strings.Contains(text, "無法取得加權指數 60 日均線") {
		t.Fatalf("摘要應說明 M 無法判斷:%s", text)
	}
	if schema := screenCanslimSchema(); schema.Properties["skip_letters"] == nil ||
		string(schema.Properties["sort_by"].Default) != `"rs_score"` ||
		string(screenStocksSchema().Properties["sort_by"].Default) != `"stock_symbol"` {
		t.Fatal("screen_canslim schema 應有 skip_letters,且改預設排序不能影響 screen_stocks")
	}
}

// TestScreenStocksCanslimFilters 驗證 screen_stocks 的 C／A／N 參數:單獨即可篩選、送進 Data API、範圍檢查、排序與摘要。
func TestScreenStocksCanslimFilters(t *testing.T) {
	yoy, high := 50.0, "2026-11-02"
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{{StockSymbol: "2330", Name: "台積電", EPSYoYPercent: &yoy, NewHighDate: &high}}}}
	for _, in := range []ScreenStocksInput{
		{MinEPSYoYPercent: ptr(25.0)}, {MinAnnualEPSGrowthPercent: ptr(0.0)}, {NewHighDays: 30, SortBy: "annual_eps_growth"},
	} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, in); err != nil {
			t.Fatalf("%+v 單獨即可篩選:%v", in, err)
		}
	}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{NewHighDays: 30, SortBy: "eps_yoy"})
	if err != nil || f.gotOpt.NewHighDays != 30 {
		t.Fatalf("new_high_days 未傳給 client:%v %+v", err, f.gotOpt)
	}
	if text := resultText(t, result); !strings.HasPrefix(text, "首筆成長指標:單季 EPS 年增 50%、近 3 年每年 EPS 年增最低 無、") || !strings.Contains(text, "最近一次 52 週新高 2026-11-02") {
		t.Fatalf("摘要缺少成長指標:%s", text)
	}
	for _, bad := range []ScreenStocksInput{
		{MinEPSYoYPercent: ptr(-101.0)}, {MinAnnualEPSGrowthPercent: ptr(10001.0)}, {NewHighDays: 366}, {NewHighDays: -1},
	} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	query := apiScreenQueryForTest(t, ScreenOptions{
		MinEPSYoYPercent: ptr(25.0), MinAnnualEPSGrowthPercent: ptr(0.0), NewHighDays: 60,
		SortBy: "eps_yoy", SortOrder: "desc", Limit: 20,
	})
	for _, want := range []string{"min_eps_yoy_percent=25", "min_annual_eps_growth_percent=0", "new_high_days=60"} {
		if !strings.Contains(query, want) {
			t.Errorf("Data API query 缺少 %s:%s", want, query)
		}
	}
	if plain := apiScreenQueryForTest(t, ScreenOptions{MaxPE: ptr(10.0), SortBy: "stock_symbol", SortOrder: "asc", Limit: 20}); strings.Contains(plain, "eps_yoy") || strings.Contains(plain, "new_high") {
		t.Fatalf("未設定時不應帶入:%s", plain)
	}
	if screenGrowthLine(ScreenedStock{}) != "" {
		t.Fatal("沒有成長指標時不應多一行")
	}
}

// TestBacktestCanslimFilters 驗證回測的 C／A／N:query、範圍檢查、回測與穩健度摘要的條件說明。
func TestBacktestCanslimFilters(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{
		MinEPSYoYPercent: ptr(25.0), MinAnnualEPSGrowthPercent: ptr(-5.0), NewHighDays: 60,
	})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	values := opt.query()
	for key, want := range map[string]string{"min_eps_yoy_percent": "25", "min_annual_eps_growth_percent": "-5", "new_high_days": "60"} {
		if values.Get(key) != want {
			t.Errorf("query %s = %q,應為 %q", key, values.Get(key), want)
		}
	}
	plain, _ := screenBacktestOptions(ScreenBacktestInput{})
	if plain.query().Has("new_high_days") || plain.query().Has("min_eps_yoy_percent") {
		t.Fatal("未設定時不應帶入 query")
	}
	for _, bad := range []ScreenBacktestInput{
		{MinEPSYoYPercent: ptr(-100.5)}, {MinAnnualEPSGrowthPercent: ptr(10001.0)}, {NewHighDays: 366}, {NewHighDays: -1},
	} {
		if _, err := screenBacktestOptions(bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	want := "已公布的最近一季單季 EPS 年增 ≥ 25%、已公布的近 3 年每年 EPS 年增 ≥ -5%、近 60 天內創 52 週新高"
	if got := strings.Join(canslimTexts(ptr(25.0), ptr(-5.0), 60), "、"); got != want {
		t.Fatalf("canslimTexts = %q", got)
	}
	if canslimTexts(nil, nil, 0) != nil {
		t.Fatal("沒有條件應為 nil")
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, want) {
		t.Fatalf("穩健度摘要缺少 CANSLIM 條件:%s", text)
	}
	e := sampleScreenBacktest()
	e.MinEPSYoYPercent, e.NewHighDays = ptr(25.0), ptr(60)
	if text := screenBacktestSummary(e); !strings.Contains(text, "已公布的最近一季單季 EPS 年增 ≥ 25%") || !strings.Contains(text, "近 60 天內創 52 週新高") {
		t.Fatalf("回測摘要缺少 CANSLIM 條件:%s", text)
	}
	if backtestFundamentalSchemas()["new_high_days"] == nil {
		t.Fatal("回測 schema 缺少 new_high_days")
	}
}
