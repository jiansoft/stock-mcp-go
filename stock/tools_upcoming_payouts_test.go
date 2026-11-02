// Package stock 測試 get_upcoming_payouts 的 API client、輸入驗證、摘要與註冊。
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

// fakePayoutsQuerier 是同時具備基本 Querier 與 UpcomingPayoutsQuerier 的假資料來源。
type fakePayoutsQuerier struct {
	fakeQuerier
	payouts   *UpcomingPayouts
	err       error
	gotMember int64
	gotDays   int
}

func (f *fakePayoutsQuerier) UpcomingPayouts(_ context.Context, memberID int64, days int) (*UpcomingPayouts, error) {
	f.gotMember, f.gotDays = memberID, days
	if f.err != nil {
		return nil, f.err
	}
	return f.payouts, nil
}

func samplePayouts() *UpcomingPayouts {
	exDate := "2026-10-22"
	return &UpcomingPayouts{
		From: "2026-11-02", To: "2026-12-01", TotalCashAmount: 3852.4,
		Items: []UpcomingPayoutItem{
			{
				PayableDate: "2026-11-11", StockSymbol: "0056", Name: "元大高股息", Quarter: "Q3", ExDate: &exDate,
				Shares: 1670, CashDividend: 1.72, CashAmount: 2872.4,
			},
			{
				PayableDate: "2026-11-20", StockSymbol: "2330", Name: "台積電", Quarter: "", Shares: 140,
				CashDividend: 7, CashAmount: 980, StockDividend: 1, StockShares: 14, Estimated: true,
			},
		},
	}
}

// TestAPIClientUpcomingPayouts 驗證路徑、query string 與 null 陣列補齊。
func TestAPIClientUpcomingPayouts(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/portfolio/upcoming-payouts" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"member_id":0,"from":"2026-11-02","to":"2026-12-01","total_cash_amount":0,"items":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	payouts, err := client.UpcomingPayouts(t.Context(), 0, 30)
	if err != nil || payouts.Items == nil || payouts.To != "2026-12-01" {
		t.Fatalf("UpcomingPayouts() = %#v, %v", payouts, err)
	}
	if gotQuery != "days=30&member_id=0" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}

	failing := NewAPIClient(server.URL+"/missing", "secret", time.Second)
	if _, err := failing.UpcomingPayouts(t.Context(), 0, 30); err == nil {
		t.Fatal("API 失敗時應回錯誤")
	}
}

// TestUpcomingPayoutsTool 驗證預設值、範圍檢查、錯誤分層與摘要內容。
func TestUpcomingPayoutsTool(t *testing.T) {
	newToolset := func(f *fakePayoutsQuerier) *upcomingPayoutsToolset {
		return &upcomingPayoutsToolset{payouts: f, logf: func(string, ...any) {}}
	}

	f := &fakePayoutsQuerier{payouts: samplePayouts()}
	result, output, err := newToolset(f).upcomingPayouts(t.Context(), nil, UpcomingPayoutsInput{})
	if err != nil {
		t.Fatalf("upcomingPayouts 不應失敗:%v", err)
	}
	if f.gotMember != 0 || f.gotDays != 30 {
		t.Fatalf("預設應為全部會員、30 天,收到 %d/%d", f.gotMember, f.gotDays)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"全部會員的持股在 2026-11-02 到 2026-12-01 有 2 筆股利入帳,現金合計約 3852 元:",
		"- 2026-11-11 入帳 0056 元大高股息(Q3,2026-10-22 除權息,1670 股)每股 1.72 元、現金 2872 元",
		"- 2026-11-20 入帳 2330 台積電(年配,140 股)每股 7 元、現金 980 元;配股 14 股(每股 1 元)(推算)",
		"未扣二代健保",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(UpcomingPayoutsOutput); !ok || out.DataKind != "upcoming_payouts" || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).upcomingPayouts(t.Context(), nil, UpcomingPayoutsInput{MemberID: 2, Days: 90}); err != nil || f.gotMember != 2 || f.gotDays != 90 {
		t.Fatalf("參數應原樣傳給 API:%v %d/%d", err, f.gotMember, f.gotDays)
	}
	for _, in := range []UpcomingPayoutsInput{{MemberID: -1}, {Days: 91}} {
		if _, _, err := newToolset(f).upcomingPayouts(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	if _, _, err := newToolset(&fakePayoutsQuerier{err: errors.New("boom")}).upcomingPayouts(t.Context(), nil, UpcomingPayoutsInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestUpcomingPayoutsTextEdgeCases 涵蓋沒有入帳、指定會員與只配股。
func TestUpcomingPayoutsTextEdgeCases(t *testing.T) {
	empty := upcomingPayoutsText(&UpcomingPayouts{MemberID: 3, From: "2026-11-02", To: "2026-12-01"})
	if !strings.Contains(empty, "會員 3的持股在 2026-11-02 到 2026-12-01 沒有股利入帳。") {
		t.Fatalf("摘要錯誤:%s", empty)
	}
	stockOnly := upcomingPayoutText(UpcomingPayoutItem{StockDividend: 0.5, StockShares: 50})
	if stockOnly != "配股 50 股(每股 0.5 元)" {
		t.Fatalf("只配股文字錯誤:%s", stockOnly)
	}
}

// TestUpcomingPayoutsToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestUpcomingPayoutsToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakePayoutsQuerier{}, nil)
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
		if tool.Name == "get_upcoming_payouts" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_upcoming_payouts 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_upcoming_payouts")
	}
}
