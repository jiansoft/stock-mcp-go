// Package stock 測試營收創歷史新高條件:即時選股與回測的參數、query string 與摘要。
package stock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestScreenStocksRevenueRecord 驗證 revenue_record 帶入 query string、算實質篩選、回應欄位解析與摘要。
func TestScreenStocksRevenueRecord(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"data_as_of":null,"stocks":[{"stock_symbol":"2330","name":"台積電","market_id":2,"industry_id":24,"revenue_record":true}]}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)
	result, err := client.ScreenStocks(t.Context(), ScreenOptions{Market: "all", RevenueRecord: true, SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
	if err != nil || len(result.Stocks) != 1 || result.Stocks[0].RevenueRecord == nil || !*result.Stocks[0].RevenueRecord {
		t.Fatalf("ScreenStocks() = %#v, %v", result, err)
	}
	if !strings.Contains(gotQuery, "revenue_record=true") {
		t.Fatalf("query 缺少 revenue_record:%s", gotQuery)
	}
	if _, err := client.ScreenStocks(t.Context(), ScreenOptions{Market: "twse", Limit: 20}); err != nil || strings.Contains(gotQuery, "revenue_record") {
		t.Fatalf("未設定時不應帶入:%s %v", gotQuery, err)
	}

	up := true
	f := &fakeStockScreener{result: &Screening{Stocks: []ScreenedStock{{StockSymbol: "2330", Name: "台積電", RevenueRecord: &up}}}}
	res, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{RevenueRecord: true})
	if err != nil || !f.gotOpt.RevenueRecord {
		t.Fatalf("營收新高本身就是篩選條件:%v %#v", err, f.gotOpt)
	}
	if summary := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(summary, "（創歷史新高）") {
		t.Errorf("摘要缺少營收新高:%s", summary)
	}
	down := false
	for record, want := range map[*bool]string{nil: "是否新高無法判斷", &down: "未創新高", &up: "創歷史新高"} {
		if got := revenueRecordText(record); got != want {
			t.Errorf("revenueRecordText = %q,應為 %q", got, want)
		}
	}
	schema := screenStocksSchema()
	if property := schema.Properties["revenue_record"]; property == nil || property.Default != nil {
		t.Fatal("schema 缺少 revenue_record 或不該有預設值")
	}
}

// TestBacktestRevenueRecord 驗證回測的 revenue_record 帶入 query string、摘要與 schema。
func TestBacktestRevenueRecord(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{RevenueRecord: true})
	if err != nil || opt.query().Get("revenue_record") != "true" {
		t.Fatalf("revenue_record 應帶入 query:%v %v", err, opt.query())
	}
	plain, err := screenBacktestOptions(ScreenBacktestInput{})
	if err != nil || plain.query().Has("revenue_record") {
		t.Fatalf("未設定時不應帶入:%v", err)
	}
	if !strings.Contains(backtestOptionsText(opt), "已公布的最新月營收創歷史新高") {
		t.Fatalf("穩健度摘要缺少營收新高:%s", backtestOptionsText(opt))
	}
	summary := screenBacktestSummary(&ScreenBacktestEnvelope{
		From: "2019-01-02", To: "2026-10-20", Market: "all", HoldDays: 20, Limit: 20, RevenueRecord: true,
	})
	if !strings.Contains(summary, "已公布的最新月營收創歷史新高") {
		t.Fatalf("回測摘要缺少營收新高:%s", summary)
	}
	if revenueRecordTexts(false) != nil {
		t.Fatal("未設定時不應有說明")
	}
	if property := screenBacktestSchema().Properties["revenue_record"]; property == nil || property.Default != nil {
		t.Fatal("schema 缺少 revenue_record 或不該有預設值")
	}
}
