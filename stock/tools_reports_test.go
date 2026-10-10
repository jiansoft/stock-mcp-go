// Package stock 測試 get_financial_reports 的 API client、輸入驗證、摘要與註冊。
package stock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeReportQuerier 是同時具備基本 Querier 與 FinancialReportQuerier 的假資料來源。
type fakeReportQuerier struct {
	fakeQuerier
	envelope  *FinancialReportEnvelope
	err       error
	gotPeriod string
	gotLimit  int
}

func (f *fakeReportQuerier) FinancialReports(_ context.Context, _ string, periodType string, limit int) (*FinancialReportEnvelope, error) {
	f.gotPeriod, f.gotLimit = periodType, limit
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleReports 取 2330 2026-Q2 的實際數值(千元)。
func sampleReports() *FinancialReportEnvelope {
	asOf := "2026-Q2"
	return &FinancialReportEnvelope{
		StockSymbol: "2330",
		PeriodType:  "quarterly",
		AmountUnit:  "thousand_twd",
		DataAsOf:    &asOf,
		IncomeStatements: []IncomeStatementItem{{
			FiscalYear: 2026, Quarter: "Q2", Source: "yahoo",
			Revenue: i64(1_270_380_250), GrossProfit: i64(860_310_695), OperatingProfit: i64(766_602_651),
			NetIncome: i64(706_780_923), EPS: ptr(27.25),
		}},
		BalanceSheets: []BalanceSheetItem{{
			FiscalYear: 2026, Quarter: "Q2", Source: "yahoo",
			TotalAssets: i64(8_000_000_000), TotalLiabilities: i64(2_000_000_000), BookValuePerShare: ptr(248.05),
		}},
		CashFlowStatements: []CashFlowStatementItem{{
			FiscalYear: 2026, Quarter: "Q2", Source: "yahoo",
			OperatingCashFlow: i64(900_000_000), InvestingCashFlow: i64(-400_000_000), FreeCashFlow: nil,
		}},
	}
}

// TestAPIClientFinancialReports 驗證路徑、query string、404 語意與空陣列補齊。
func TestAPIClientFinancialReports(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/2330/financial-reports":
			gotQuery = r.URL.RawQuery
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","period_type":"annual","amount_unit":"thousand_twd","data_as_of":null,"income_statements":null,"balance_sheets":null,"cash_flow_statements":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	reports, err := client.FinancialReports(t.Context(), "2330", "annual", 5)
	if err != nil || reports.IncomeStatements == nil || reports.BalanceSheets == nil || reports.CashFlowStatements == nil {
		t.Fatalf("FinancialReports() = %#v, %v", reports, err)
	}
	if gotQuery != "limit=5&period_type=annual" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
	if _, err := client.FinancialReports(t.Context(), "9999", "quarterly", 8); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
}

// TestFinancialReportTool 驗證預設參數、範圍檢查、錯誤分層與摘要內容。
func TestFinancialReportTool(t *testing.T) {
	newToolset := func(f *fakeReportQuerier) *financialReportToolset {
		return &financialReportToolset{reports: f, logf: func(string, ...any) {}}
	}

	f := &fakeReportQuerier{envelope: sampleReports()}
	result, output, err := newToolset(f).financialReports(t.Context(), nil, FinancialReportInput{Symbol: "2330"})
	if err != nil {
		t.Fatalf("financialReports 不應失敗:%v", err)
	}
	if f.gotPeriod != "quarterly" || f.gotLimit != 8 {
		t.Fatalf("預設應為 quarterly/8,收到 %s/%d", f.gotPeriod, f.gotLimit)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"股票 2330 三大財報(單季,最新 2026-Q2",
		"損益表 2026-Q2:營收 12703.80 億元、毛利率 67.72%、營業利益率 60.34%、稅後淨利 7067.81 億元、EPS 27.25 元",
		"負債比 25.00%、每股淨值 248.05 元",
		"營業現金流 9000.00 億元、投資現金流 -4000.00 億元、自由現金流 無",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(FinancialReportOutput); !ok || out.DataKind != "financial_reports" || out.StockSymbol != "2330" {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).financialReports(t.Context(), nil, FinancialReportInput{Symbol: "2330", PeriodType: "annual", Limit: 3}); err != nil || f.gotPeriod != "annual" || f.gotLimit != 3 {
		t.Fatalf("annual/3 應原樣傳給 API:%v %s/%d", err, f.gotPeriod, f.gotLimit)
	}
	if _, _, err := newToolset(f).financialReports(t.Context(), nil, FinancialReportInput{Symbol: "2330", PeriodType: "all"}); err == nil || !strings.Contains(err.Error(), "period_type") {
		t.Fatalf("period_type 不合法應回錯誤:%v", err)
	}
	if _, _, err := newToolset(f).financialReports(t.Context(), nil, FinancialReportInput{Symbol: "2330", Limit: 41}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit 超界應回錯誤:%v", err)
	}
	if _, _, err := newToolset(f).financialReports(t.Context(), nil, FinancialReportInput{Symbol: " "}); err == nil {
		t.Fatal("空白代號應回錯誤")
	}
	if _, _, err := newToolset(&fakeReportQuerier{err: ErrStockNotFound}).financialReports(t.Context(), nil, FinancialReportInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeReportQuerier{err: errors.New("boom")}).financialReports(t.Context(), nil, FinancialReportInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestFinancialReportSummaryWithoutData 沒有任何報表時仍給出可讀摘要;年度模式標示年度。
func TestFinancialReportSummaryWithoutData(t *testing.T) {
	summary := financialReportSummary(&FinancialReportEnvelope{StockSymbol: "00679B", PeriodType: "annual"})
	if !strings.Contains(summary, "股票 00679B 尚無年度三大財報資料。") {
		t.Fatalf("摘要錯誤:%s", summary)
	}
	if percentOf(i64(1), i64(0)) != "無" || percentOf(nil, i64(1)) != "無" {
		t.Fatal("分母為 0 或缺值時比率應為「無」")
	}
}

// TestFinancialReportToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestFinancialReportToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeReportQuerier{}, nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), t1, nil); err != nil {
		t.Fatalf("server.Connect:%v", err)
	}
	cs, err := client.Connect(t.Context(), t2, nil)
	if err != nil {
		t.Fatalf("client.Connect:%v", err)
	}
	defer cs.Close()
	found := false
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if tool.Name == "get_financial_reports" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_financial_reports 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_financial_reports")
	}
}
