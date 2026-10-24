// Package stock 測試估值上限(回測與選股的 max_pe、max_pb、依本益比排序)與持股的自由現金流警示。
package stock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestBacktestValuationCaps 驗證回測 max_pe／max_pb 的範圍、query string、摘要與 schema(不帶預設值)。
func TestBacktestValuationCaps(t *testing.T) {
	pe, pb := 15.0, 1.5
	opt, err := screenBacktestOptions(ScreenBacktestInput{MaxPE: &pe, MaxPB: &pb})
	if err != nil {
		t.Fatalf("screenBacktestOptions: %v", err)
	}
	query := opt.query()
	if query.Get("max_pe") != "15" || query.Get("max_pb") != "1.5" {
		t.Fatalf("query 錯誤:%v", query)
	}
	plain, err := screenBacktestOptions(ScreenBacktestInput{})
	if err != nil || plain.query().Has("max_pe") || plain.query().Has("max_pb") {
		t.Fatalf("未設定時不應帶入:%v %v", err, plain.query())
	}
	for _, in := range []ScreenBacktestInput{
		{MaxPE: ptr(-1.0)}, {MaxPE: ptr(10001.0)}, {MaxPB: ptr(1001.0)},
	} {
		if _, err := screenBacktestOptions(in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	if text := backtestOptionsText(opt); !strings.Contains(text, "換股當天本益比 ≤ 15 倍(虧損不選)") ||
		!strings.Contains(text, "換股當天股價淨值比 ≤ 1.5 倍") {
		t.Fatalf("穩健度摘要缺少條件:%s", text)
	}
	summary := screenBacktestSummary(&ScreenBacktestEnvelope{
		From: "2019-01-02", To: "2026-10-20", Market: "all", HoldDays: 20, Limit: 20, MaxPE: &pe,
	})
	if !strings.Contains(summary, "換股當天本益比 ≤ 15 倍") || strings.Contains(summary, "股價淨值比 ≤") {
		t.Fatalf("回測摘要條件錯誤:%s", summary)
	}
	if valuationCapTexts(nil, nil) != nil {
		t.Fatal("未設定時不應有說明")
	}
	for _, name := range []string{"max_pe", "max_pb"} {
		if property := screenBacktestSchema().Properties[name]; property == nil || property.Default != nil {
			t.Fatalf("schema 缺少 %s 或不該有預設值", name)
		}
	}
}

// TestScreenValuationCaps 驗證選股 max_pe／max_pb 單獨即可篩選、傳給 client、依本益比排序與摘要。
func TestScreenValuationCaps(t *testing.T) {
	fixture := &Screening{Stocks: []ScreenedStock{{
		StockSymbol: "2330", Name: "台積電", PERatio: ptr(18.5), PBRatio: ptr(5.2), RatioDate: ptr("2026-10-20"),
	}}}
	f := &fakeStockScreener{result: fixture}
	res, out, err := newScreenToolset(f).screenStocks(t.Context(), nil,
		ScreenStocksInput{MaxPE: ptr(20.0), MaxPB: ptr(6.0), SortBy: "pe_ratio"})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	if f.gotOpt.MaxPE == nil || *f.gotOpt.MaxPE != 20 || f.gotOpt.MaxPB == nil || *f.gotOpt.MaxPB != 6 || f.gotOpt.SortBy != "pe_ratio" {
		t.Fatalf("估值條件未傳給 client:%+v", f.gotOpt)
	}
	if text := summaryOf(t, res); !strings.Contains(text, "本益比 18.5、股價淨值比 5.2（2026-10-20）") {
		t.Fatalf("摘要缺少估值:%s", text)
	}
	stock := roundTripJSON(t, out)["stocks"].([]any)[0].(map[string]any)
	if stock["pe_ratio"] != 18.5 || stock["pb_ratio"] != 5.2 || stock["ratio_date"] != "2026-10-20" {
		t.Fatalf("估值欄位未原樣保留:%v", stock)
	}
	for _, in := range []ScreenStocksInput{{MaxPE: ptr(-1.0)}, {MaxPB: ptr(1001.0)}} {
		if _, _, err := newScreenToolset(&fakeStockScreener{result: fixture}).screenStocks(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	schema := screenStocksSchema()
	if schema.Properties["max_pe"] == nil || schema.Properties["max_pe"].Default != nil || schema.Properties["max_pb"] == nil {
		t.Fatal("schema 缺少 max_pe／max_pb 或不該有預設值")
	}
	enum := schema.Properties["sort_by"].Enum
	if enum[len(enum)-2] != "pe_ratio" || enum[len(enum)-1] != "pb_ratio" {
		t.Fatalf("sort_by enum 缺少估值:%v", enum)
	}
	query := apiScreenQueryForTest(t, ScreenOptions{MaxPE: ptr(12.5), MaxPB: ptr(0.8), SortBy: "pb_ratio", SortOrder: "asc", Limit: 20})
	if !strings.Contains(query, "max_pe=12.5") || !strings.Contains(query, "max_pb=0.8") {
		t.Fatalf("Data API query 缺少估值上限:%s", query)
	}
}

// apiScreenQueryForTest 以假 Data API 執行 ScreenStocks,回傳實際送出的 query string。
func apiScreenQueryForTest(t *testing.T, opt ScreenOptions) string {
	t.Helper()
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data_as_of":null,"stocks":[]}`))
	}))
	defer server.Close()
	if _, err := NewAPIClient(server.URL, "secret", time.Second).ScreenStocks(t.Context(), opt); err != nil {
		t.Fatalf("ScreenStocks: %v", err)
	}
	return query
}

// TestPortfolioNegativeFreeCashFlowWarning 驗證自由現金流警示的中文名稱與摘要裡的金額。
func TestPortfolioNegativeFreeCashFlowWarning(t *testing.T) {
	holding := warnedHolding()
	holding.Warnings = append(holding.Warnings, "negative_free_cash_flow")
	holding.FreeCashFlowTTM = ptr(int64(-123_456))
	summary := samplePortfolio()
	summary.WarningSymbols = 1
	summary.Holdings = append(summary.Holdings, holding)
	text := portfolioSummaryText(summary)
	if !strings.Contains(text, "營業虧損、自由現金流為負;") || !strings.Contains(text, "近四季自由現金流 -1.2 億") {
		t.Fatalf("摘要缺少自由現金流警示:%s", text)
	}
}
