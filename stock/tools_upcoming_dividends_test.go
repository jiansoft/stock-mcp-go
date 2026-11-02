// Package stock 測試 get_upcoming_dividends 的 API client、輸入驗證、摘要與註冊。
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

// fakeUpcomingQuerier 是同時具備基本 Querier 與 UpcomingDividendsQuerier 的假資料來源。
type fakeUpcomingQuerier struct {
	fakeQuerier
	upcoming  *UpcomingDividends
	err       error
	gotMember int64
	gotDays   int
}

func (f *fakeUpcomingQuerier) UpcomingDividends(_ context.Context, memberID int64, days int) (*UpcomingDividends, error) {
	f.gotMember, f.gotDays = memberID, days
	if f.err != nil {
		return nil, f.err
	}
	return f.upcoming, nil
}

func sampleUpcoming() *UpcomingDividends {
	return &UpcomingDividends{
		From: "2026-11-02", To: "2026-11-15", TotalCashAmount: 7500,
		Items: []UpcomingDividendItem{
			{
				ExDate: "2026-11-05", StockSymbol: "2330", Name: "台積電", Shares: 1500, CashDividend: 5, CashAmount: 7500,
				FillHistory: &DividendFillSummary{Events: 5, Filled: 4, AverageTradingDaysToFill: ptr(3.0)},
			},
			{
				ExDate: "2026-11-10", StockSymbol: "2412", Name: "中華電", Shares: 1000, StockDividend: 0.5, StockShares: 50,
				FillPeer: &DividendFillBucket{Label: "3～5%", MedianTradingDaysToFill: ptr(15.0)},
			},
		},
	}
}

// TestAPIClientUpcomingDividends 驗證路徑、query string 與 null 陣列補齊。
func TestAPIClientUpcomingDividends(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/portfolio/upcoming-dividends" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"member_id":0,"from":"2026-11-02","to":"2026-11-15","total_cash_amount":0,"items":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	upcoming, err := client.UpcomingDividends(t.Context(), 0, 14)
	if err != nil || upcoming.Items == nil || upcoming.To != "2026-11-15" {
		t.Fatalf("UpcomingDividends() = %#v, %v", upcoming, err)
	}
	if gotQuery != "days=14&member_id=0" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}

	failing := NewAPIClient(server.URL+"/missing", "secret", time.Second)
	if _, err := failing.UpcomingDividends(t.Context(), 0, 14); err == nil {
		t.Fatal("API 失敗時應回錯誤")
	}
}

// TestUpcomingDividendsTool 驗證預設值、範圍檢查、錯誤分層與摘要內容。
func TestUpcomingDividendsTool(t *testing.T) {
	newToolset := func(f *fakeUpcomingQuerier) *upcomingDividendsToolset {
		return &upcomingDividendsToolset{upcoming: f, logf: func(string, ...any) {}}
	}

	f := &fakeUpcomingQuerier{upcoming: sampleUpcoming()}
	result, output, err := newToolset(f).upcomingDividends(t.Context(), nil, UpcomingDividendsInput{})
	if err != nil {
		t.Fatalf("upcomingDividends 不應失敗:%v", err)
	}
	if f.gotMember != 0 || f.gotDays != 14 {
		t.Fatalf("預設應為全部會員、14 天,收到 %d/%d", f.gotMember, f.gotDays)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"全部會員的持股在 2026-11-02 到 2026-11-15 有 2 筆已公告的除權息,預估現金入帳約 7500 元:",
		"- 2026-11-05 2330 台積電(1500 股)除息 5 元、預估入帳 7500 元;近 5 年填息 4/5 次、平均 3 個交易日",
		"- 2026-11-10 2412 中華電(1000 股)除權 0.5 元、預估配股 50 股;同殖利率組(3～5%)填息中位數 15 個交易日",
		"未扣二代健保",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(UpcomingDividendsOutput); !ok || out.DataKind != "upcoming_dividends" || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).upcomingDividends(t.Context(), nil, UpcomingDividendsInput{MemberID: 2, Days: 60}); err != nil || f.gotMember != 2 || f.gotDays != 60 {
		t.Fatalf("參數應原樣傳給 API:%v %d/%d", err, f.gotMember, f.gotDays)
	}
	for _, in := range []UpcomingDividendsInput{{MemberID: -1}, {Days: 61}} {
		if _, _, err := newToolset(f).upcomingDividends(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	if _, _, err := newToolset(&fakeUpcomingQuerier{err: errors.New("boom")}).upcomingDividends(t.Context(), nil, UpcomingDividendsInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestUpcomingDividendsTextEdgeCases 涵蓋沒有事件、指定會員與同一天除息又除權。
func TestUpcomingDividendsTextEdgeCases(t *testing.T) {
	empty := upcomingDividendsText(&UpcomingDividends{MemberID: 3, From: "2026-11-02", To: "2026-11-15"})
	if !strings.Contains(empty, "會員 3的持股在 2026-11-02 到 2026-11-15 沒有已公告的除權息。") {
		t.Fatalf("摘要錯誤:%s", empty)
	}
	both := upcomingDividendText(UpcomingDividendItem{CashDividend: 1.5, CashAmount: 1500, StockDividend: 0.5, StockShares: 50})
	if both != "除息 1.5 元、預估入帳 1500 元;除權 0.5 元、預估配股 50 股" {
		t.Fatalf("同日除息除權文字錯誤:%s", both)
	}
}

// TestUpcomingDividendsToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestUpcomingDividendsToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeUpcomingQuerier{}, nil)
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
		if tool.Name == "get_upcoming_dividends" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_upcoming_dividends 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_upcoming_dividends")
	}
}
