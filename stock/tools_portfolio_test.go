// Package stock 測試 get_portfolio_summary 的 API client、輸入驗證、摘要與註冊。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakePortfolioQuerier 是同時具備基本 Querier 與 PortfolioQuerier 的假資料來源。
type fakePortfolioQuerier struct {
	fakeQuerier
	summary   *PortfolioSummary
	err       error
	gotMember int64
}

func (f *fakePortfolioQuerier) PortfolioSummary(_ context.Context, memberID int64) (*PortfolioSummary, error) {
	f.gotMember = memberID
	if f.err != nil {
		return nil, f.err
	}
	return f.summary, nil
}

// samplePortfolio 是兩個產業、兩檔持股的示意資料;第二檔沒有報價。
func samplePortfolio() *PortfolioSummary {
	return &PortfolioSummary{
		MemberID: 0, DataAsOf: ptr("2026-10-08"), RSAsOf: ptr("2026-10-14"), ChipAsOf: ptr("2026-10-13"), Symbols: 2,
		MarketValue: 100000, Cost: 90000, UnrealizedGain: 10000, UnrealizedGainPercent: ptr(11.11), DividendsReceived: 500,
		WeightedDividendYield: &WeightedMetric{Value: 4.5, CoveragePercent: 100},
		Industries: []PortfolioIndustry{
			{IndustryID: ptr(24), IndustryName: "半導體業", MarketValue: 100000, WeightPercent: 100, Symbols: 1},
			{IndustryName: "", MarketValue: 0, WeightPercent: 0, Symbols: 1},
		},
		Holdings: []PortfolioHolding{
			{StockSymbol: "2330", Name: "台積電", Shares: 2000, Cost: 90000, ClosingPrice: ptr(50.0), MarketValue: ptr(100000.0),
				WeightPercent: ptr(100.0), UnrealizedGain: ptr(10000.0), UnrealizedGainPercent: ptr(11.11), DividendsReceived: 500,
				DividendYieldPercent: ptr(4.5), FScore: ptr(6), RSScore: ptr(79), ForeignStreak: ptr(-6), TrustStreak: ptr(11)},
			{StockSymbol: "9999", Name: "下市股", Shares: 100},
		},
	}
}

// TestAPIClientPortfolioSummary 驗證路徑、query string 與 null 陣列補齊。
func TestAPIClientPortfolioSummary(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/portfolio/summary" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"member_id":2,"data_as_of":null,"symbols":0,"market_value":0,"cost":0,"unrealized_gain":0,` +
			`"unrealized_gain_percent":null,"dividends_received":0,"weighted_dividend_yield":null,"weighted_f_score":null,"industries":null,"holdings":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	summary, err := client.PortfolioSummary(t.Context(), 2)
	if err != nil || summary.Holdings == nil || summary.Industries == nil || summary.MemberID != 2 {
		t.Fatalf("PortfolioSummary() = %#v, %v", summary, err)
	}
	if gotQuery != "member_id=2" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
}

// TestPortfolioSummaryTool 驗證預設會員、負數檢查、錯誤分層與摘要內容。
func TestPortfolioSummaryTool(t *testing.T) {
	newToolset := func(f *fakePortfolioQuerier) *portfolioToolset {
		return &portfolioToolset{portfolio: f, logf: func(string, ...any) {}}
	}

	f := &fakePortfolioQuerier{summary: samplePortfolio()}
	result, output, err := newToolset(f).portfolioSummary(t.Context(), nil, PortfolioSummaryInput{})
	if err != nil {
		t.Fatalf("portfolioSummary 不應失敗:%v", err)
	}
	if f.gotMember != 0 {
		t.Fatalf("預設應為 0,收到 %d", f.gotMember)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"全部會員目前持有 2 檔(報價截至 2026-10-08):市值 100000 元、成本 90000 元,未實現損益 10000 元(11.11%),目前持股累計已領股利 500 元。",
		"市值加權殖利率 4.5%(覆蓋 100% 市值);市值加權 F 分數 無。",
		"產業分布:半導體業 100%(1 檔)、未分類 0%(1 檔)。",
		"- 2330 台積電:2000 股,權重 100%,未實現 11.11%,殖利率 4.5%,本益比 無,F 分數 6,RS 79,外資連續賣超 6 天,投信連續買超 11 天",
		"- 9999 下市股:100 股,權重 無,未實現 無,殖利率 無,本益比 無,F 分數 無,RS 無,外資連續天數無資料,投信連續天數無資料",
		"RS(相對強度 1–99)截至 2026-10-14、法人連續天數截至 2026-10-13。",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(PortfolioSummaryOutput); !ok || out.DataKind != "portfolio_summary" || out.Symbols != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).portfolioSummary(t.Context(), nil, PortfolioSummaryInput{MemberID: 3}); err != nil || f.gotMember != 3 {
		t.Fatalf("member_id 應原樣傳給 API:%v %d", err, f.gotMember)
	}
	if _, _, err := newToolset(f).portfolioSummary(t.Context(), nil, PortfolioSummaryInput{MemberID: -1}); err == nil || !strings.Contains(err.Error(), "member_id") {
		t.Fatalf("負數應回錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakePortfolioQuerier{err: errors.New("boom")}).portfolioSummary(t.Context(), nil, PortfolioSummaryInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestPortfolioSummaryTextEdgeCases 涵蓋沒有持股、指定會員,以及產業與持股清單上限。
func TestPortfolioSummaryTextEdgeCases(t *testing.T) {
	if got := portfolioSummaryText(&PortfolioSummary{MemberID: 3}); !strings.Contains(got, "會員 3目前沒有持股。") {
		t.Fatalf("摘要錯誤:%s", got)
	}

	p := &PortfolioSummary{MemberID: 1, Symbols: 12, WeightedFScore: &WeightedMetric{Value: 6.5, CoveragePercent: 80}}
	for i := range 12 {
		p.Industries = append(p.Industries, PortfolioIndustry{IndustryName: fmt.Sprintf("產業%d", i), Symbols: 1})
		p.Holdings = append(p.Holdings, PortfolioHolding{StockSymbol: fmt.Sprintf("10%02d", i), Name: "測試", Shares: 1000})
	}
	got := portfolioSummaryText(p)
	for _, want := range []string{"會員 1目前持有 12 檔", "市值加權 F 分數 6.5(覆蓋 80% 市值)", "等 12 個產業", "…其餘 2 檔見結構化資料。"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要缺少 %q:%s", want, got)
		}
	}
	if count := strings.Count(got, "\n- "); count != maxHoldingsInSummary {
		t.Errorf("持股清單應最多 %d 筆,實際 %d", maxHoldingsInSummary, count)
	}
}

// TestPortfolioSummaryToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestPortfolioSummaryToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakePortfolioQuerier{}, nil)
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
		if tool.Name == "get_portfolio_summary" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_portfolio_summary 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_portfolio_summary")
	}
}
