// Package stock 測試財報三率條件:即時選股與回測的參數、query string、驗證與摘要。
package stock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestAPIClientScreenStocksMargins 驗證營業利益率與三率三升帶入 query string,且回應欄位解析正確。
func TestAPIClientScreenStocksMargins(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"data_as_of":null,"stocks":[{"stock_symbol":"2330","name":"台積電","market_id":2,"industry_id":24,` +
			`"operating_margin_percent":60.34,"net_margin_percent":55.64,"margins_yoy_up":true,"margin_period":"2026-Q2"}]}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)
	result, err := client.ScreenStocks(t.Context(), ScreenOptions{
		Market: "all", MinOperatingMarginPercent: ptr(0.0), MarginsYoYUp: true, SortBy: "operating_margin", SortOrder: "desc", Limit: 20,
	})
	if err != nil || len(result.Stocks) != 1 {
		t.Fatalf("ScreenStocks() = %#v, %v", result, err)
	}
	stock := result.Stocks[0]
	if *stock.OperatingMarginPercent != 60.34 || *stock.NetMarginPercent != 55.64 || !*stock.MarginsYoYUp || *stock.MarginPeriod != "2026-Q2" {
		t.Fatalf("三率欄位解析錯誤:%#v", stock)
	}
	for _, want := range []string{"min_operating_margin_percent=0", "margins_yoy_up=true", "sort_by=operating_margin"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query 缺少 %s:%s", want, gotQuery)
		}
	}
	if _, err := client.ScreenStocks(t.Context(), ScreenOptions{Market: "twse", Limit: 20}); err != nil || strings.Contains(gotQuery, "margins_yoy_up") || strings.Contains(gotQuery, "operating_margin") {
		t.Fatalf("未設定的三率條件不應帶入 query:%s %v", gotQuery, err)
	}
}

// TestScreenStocksMarginFilters 驗證三率條件算實質篩選、範圍檢查、排序白名單與摘要內容。
func TestScreenStocksMarginFilters(t *testing.T) {
	up, down := true, false
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{{
		StockSymbol: "2330", Name: "台積電", OperatingMarginPercent: ptr(60.34), NetMarginPercent: ptr(55.64),
		MarginsYoYUp: &up, MarginPeriod: ptr("2026-Q2"),
	}}}}
	result, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MarginsYoYUp: true, SortBy: "operating_margin", SortOrder: "desc"})
	if err != nil {
		t.Fatalf("三率三升本身就是篩選條件:%v", err)
	}
	if !f.gotOpt.MarginsYoYUp || f.gotOpt.SortBy != "operating_margin" {
		t.Fatalf("選項未傳給 API:%#v", f.gotOpt)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"營業利益率 60.34%", "淨利率 55.64%", "三率三升（2026-Q2）"} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{MinOperatingMarginPercent: ptr(-5.0)}); err != nil || *f.gotOpt.MinOperatingMarginPercent != -5 {
		t.Fatalf("營業利益率門檻應原樣傳給 API:%v %#v", err, f.gotOpt)
	}
	for _, in := range []ScreenStocksInput{
		{MinOperatingMarginPercent: ptr(100.5)},
		{MinOperatingMarginPercent: ptr(-10000.5)},
		{},
	} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, in); err == nil {
			t.Errorf("%#v 應回錯誤", in)
		}
	}
	if got := marginsYoYText(nil, nil); got != "三率三升無法判斷" {
		t.Errorf("marginsYoYText(nil) = %q", got)
	}
	if got := marginsYoYText(&down, ptr("2026-Q1")); got != "非三率三升（2026-Q1）" {
		t.Errorf("marginsYoYText(false) = %q", got)
	}
	schema, err := json.Marshal(screenStocksSchema())
	if err != nil || !strings.Contains(string(schema), "min_operating_margin_percent") || !strings.Contains(string(schema), "margins_yoy_up") || !strings.Contains(string(schema), `"operating_margin"`) {
		t.Fatalf("schema 缺少三率條件:%v %s", err, schema)
	}
}

// TestBacktestMarginFilters 驗證回測三率與負債比門檻的驗證、query string、摘要與 schema。
func TestBacktestMarginFilters(t *testing.T) {
	in := ScreenBacktestInput{
		MinGrossMarginPercent: ptr(30.0), MinOperatingMarginPercent: ptr(0.0), MarginsYoYUp: true, MaxDebtRatioPercent: ptr(60.0),
	}
	opt, err := screenBacktestOptions(in)
	if err != nil {
		t.Fatalf("screenBacktestOptions 不應失敗:%v", err)
	}
	values := opt.query()
	for key, want := range map[string]string{
		"min_gross_margin_percent": "30", "min_operating_margin_percent": "0", "margins_yoy_up": "true", "max_debt_ratio_percent": "60",
	} {
		if values.Get(key) != want {
			t.Errorf("query %s = %q,應為 %q", key, values.Get(key), want)
		}
	}
	plain, err := screenBacktestOptions(ScreenBacktestInput{})
	if err != nil || plain.query().Has("margins_yoy_up") || plain.query().Has("max_debt_ratio_percent") {
		t.Fatalf("未設定的三率條件不應帶入 query:%v", err)
	}
	for _, bad := range []ScreenBacktestInput{
		{MinGrossMarginPercent: ptr(100.1)},
		{MinOperatingMarginPercent: ptr(-10001.0)},
		{MaxDebtRatioPercent: ptr(-1.0)},
		{MaxDebtRatioPercent: ptr(1000.5)},
	} {
		if _, err := screenBacktestOptions(bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	texts := strings.Join(marginFilterTexts(ptr(30.0), ptr(10.0), true, ptr(60.0)), "、")
	want := "最近一季毛利率 ≥ 30%、最近一季營業利益率 ≥ 10%、三率三升(毛利率、營業利益率、淨利率都高於去年同季)、最近一季負債比 ≤ 60%"
	if texts != want {
		t.Fatalf("marginFilterTexts = %q", texts)
	}
	if got := marginFilterTexts(nil, nil, false, nil); got != nil {
		t.Fatalf("沒有條件應為 nil:%v", got)
	}
	if !strings.Contains(backtestOptionsText(opt), "三率三升") {
		t.Fatalf("穩健度摘要缺少三率條件:%s", backtestOptionsText(opt))
	}
	summary := screenBacktestSummary(&ScreenBacktestEnvelope{
		From: "2019-01-02", To: "2026-10-20", Market: "all", HoldDays: 20, Limit: 20, MarginsYoYUp: true, MaxDebtRatioPercent: ptr(50.0),
	})
	if !strings.Contains(summary, "三率三升") || !strings.Contains(summary, "最近一季負債比 ≤ 50%") {
		t.Fatalf("回測摘要缺少三率條件:%s", summary)
	}
	schema := screenBacktestSchema()
	for _, key := range []string{"min_gross_margin_percent", "min_operating_margin_percent", "margins_yoy_up", "max_debt_ratio_percent"} {
		if property := schema.Properties[key]; property == nil || property.Default != nil {
			t.Errorf("schema 缺少 %s 或不該有預設值", key)
		}
	}
}
