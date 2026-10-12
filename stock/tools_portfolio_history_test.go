// Package stock 測試 get_portfolio_history 的 API client、輸入驗證、摘要與註冊。
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

// fakeHistoryQuerier 是同時具備基本 Querier 與 PortfolioHistoryQuerier 的假資料來源。
type fakeHistoryQuerier struct {
	fakeQuerier
	history   *PortfolioHistory
	err       error
	gotMember int64
	gotMonths int
}

func (f *fakeHistoryQuerier) PortfolioHistory(_ context.Context, memberID int64, months int) (*PortfolioHistory, error) {
	f.gotMember, f.gotMonths = memberID, months
	if f.err != nil {
		return nil, f.err
	}
	return f.history, nil
}

func sampleHistory() *PortfolioHistory {
	return &PortfolioHistory{
		MemberID: 0, DataAsOf: ptr("2026-10-08"), From: ptr("2025-10-08"),
		StartValue: ptr(3103570.0), EndValue: ptr(6185430.15), Buys: ptr(1066329.5), Sells: ptr(587300.0),
		ReturnPercent: ptr(73.47), MaxDrawdownPercent: ptr(-8.01), MaxDrawdownPeak: ptr("2026-07-16"), MaxDrawdownTrough: ptr("2026-07-29"),
		BenchmarkReturnPercent: ptr(82.21), ExcessReturnPercent: ptr(-8.74),
		Months: []PortfolioMonth{
			{Month: "2026-09", EndValue: 6000000, ReturnPercent: 3.2, BenchmarkReturnPercent: ptr(4.1)},
			{Month: "2026-10", EndValue: 6185430.15, ReturnPercent: -1.5},
		},
	}
}

// TestAPIClientPortfolioHistory 驗證路徑、query string 與 null 陣列補齊。
func TestAPIClientPortfolioHistory(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/portfolio/history" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"member_id":1,"data_as_of":null,"from":null,"start_value":null,"end_value":null,"buys":null,"sells":null,` +
			`"return_percent":null,"annualized_return_percent":null,"max_drawdown_percent":null,"max_drawdown_peak":null,` +
			`"max_drawdown_trough":null,"benchmark_return_percent":null,"excess_return_percent":null,"months":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	history, err := client.PortfolioHistory(t.Context(), 1, 6)
	if err != nil || history.Months == nil || history.ReturnPercent != nil || history.MemberID != 1 {
		t.Fatalf("PortfolioHistory() = %#v, %v", history, err)
	}
	if gotQuery != "member_id=1&months=6" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
}

// TestPortfolioHistoryTool 驗證預設值、範圍檢查、錯誤分層與摘要內容。
func TestPortfolioHistoryTool(t *testing.T) {
	newToolset := func(f *fakeHistoryQuerier) *portfolioHistoryToolset {
		return &portfolioHistoryToolset{history: f, logf: func(string, ...any) {}}
	}

	f := &fakeHistoryQuerier{history: sampleHistory()}
	result, output, err := newToolset(f).portfolioHistory(t.Context(), nil, PortfolioHistoryInput{})
	if err != nil {
		t.Fatalf("portfolioHistory 不應失敗:%v", err)
	}
	if f.gotMember != 0 || f.gotMonths != 12 {
		t.Fatalf("預設應為全部會員、12 個月,收到 %d/%d", f.gotMember, f.gotMonths)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"全部會員持股 2025-10-08 至 2026-10-08 的時間加權報酬 73.47%;同期加權指數 82.21%,超額報酬 -8.74 個百分點。",
		"市值 3103570 → 6185430.15 元,期間投入 1066329.5 元、賣出 587300 元。最大回撤 -8.01%(2026-07-16 高點 → 2026-07-29 低點)。",
		"- 2026-09:3.2%(4.1%)",
		"- 2026-10:-1.5%(無%)",
		"不含現金股利",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(PortfolioHistoryOutput); !ok || out.DataKind != "portfolio_history" || len(out.Months) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).portfolioHistory(t.Context(), nil, PortfolioHistoryInput{MemberID: 2, Months: 120}); err != nil || f.gotMember != 2 || f.gotMonths != 120 {
		t.Fatalf("參數應原樣傳給 API:%v %d/%d", err, f.gotMember, f.gotMonths)
	}
	for _, in := range []PortfolioHistoryInput{{MemberID: -1}, {Months: 121}} {
		if _, _, err := newToolset(f).portfolioHistory(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	if _, _, err := newToolset(&fakeHistoryQuerier{err: errors.New("boom")}).portfolioHistory(t.Context(), nil, PortfolioHistoryInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestPortfolioHistoryTextEdgeCases 涵蓋沒有歷史、年化、沒有回撤與月份上限。
func TestPortfolioHistoryTextEdgeCases(t *testing.T) {
	if got := portfolioHistoryText(&PortfolioHistory{MemberID: 3}, 6); !strings.Contains(got, "會員 3近 6 個月沒有足夠的市值歷史") {
		t.Fatalf("摘要錯誤:%s", got)
	}

	h := sampleHistory()
	h.AnnualizedReturnPercent = ptr(25.5)
	h.MaxDrawdownPeak, h.MaxDrawdownTrough = nil, nil
	h.Months = nil
	for i := range 15 {
		h.Months = append(h.Months, PortfolioMonth{Month: fmt.Sprintf("m%02d", i), ReturnPercent: float64(i)})
	}
	got := portfolioHistoryText(h, 36)
	for _, want := range []string{"(年化 25.5%)", "期間沒有回撤。", "- m14:14%"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要缺少 %q:%s", want, got)
		}
	}
	if strings.Contains(got, "- m02:") || strings.Count(got, "\n- ") != maxMonthsInSummary {
		t.Errorf("月份應只列最近 %d 個:%s", maxMonthsInSummary, got)
	}
}

// TestPortfolioHistoryToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestPortfolioHistoryToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeHistoryQuerier{}, nil)
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
		if tool.Name == "get_portfolio_history" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_portfolio_history 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_portfolio_history")
	}
}
