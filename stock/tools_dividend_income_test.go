// Package stock 測試 get_dividend_income 的 API client、輸入驗證、摘要與註冊。
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

// fakeIncomeQuerier 是同時具備基本 Querier 與 DividendIncomeQuerier 的假資料來源。
type fakeIncomeQuerier struct {
	fakeQuerier
	income    *DividendIncome
	err       error
	gotMember int64
	gotYear   int
}

func (f *fakeIncomeQuerier) DividendIncome(_ context.Context, memberID int64, year int) (*DividendIncome, error) {
	f.gotMember, f.gotYear = memberID, year
	if f.err != nil {
		return nil, f.err
	}
	return f.income, nil
}

func sampleIncome() *DividendIncome {
	months := make([]DividendIncomeMonth, 12)
	for i := range months {
		months[i].Month = i + 1
	}
	months[0] = DividendIncomeMonth{Month: 1, CashAmount: 1500, ReceivedCashAmount: 1500, Payouts: 2, LastYearCashAmount: 1200}
	months[5] = DividendIncomeMonth{Month: 6, LastYearCashAmount: 300}
	months[10] = DividendIncomeMonth{Month: 11, CashAmount: 3852.4, ReceivedCashAmount: 2872.4, Payouts: 2, StockShares: 14}
	months[11] = DividendIncomeMonth{Month: 12, CashAmount: 980, EstimatedCashAmount: 980, Payouts: 1}
	return &DividendIncome{
		Year: 2026, AsOf: "2026-11-03", TotalCashAmount: 6332.4, ReceivedCashAmount: 4372.4, EstimatedCashAmount: 980,
		TotalStockShares: 14, LastYearCashAmount: 5000, LastYearSamePeriodCashAmount: 4100, Months: months,
	}
}

// TestAPIClientDividendIncome 驗證路徑、query string(省略或指定年度)與 null 陣列補齊。
func TestAPIClientDividendIncome(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/portfolio/dividend-income" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"member_id":0,"year":2026,"as_of":"2026-11-03","months":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	income, err := client.DividendIncome(t.Context(), 0, 0)
	if err != nil || income.Months == nil || income.Year != 2026 {
		t.Fatalf("DividendIncome() = %#v, %v", income, err)
	}
	if gotQuery != "member_id=0" {
		t.Fatalf("省略年度時 query string 錯誤:%q", gotQuery)
	}
	if _, err := client.DividendIncome(t.Context(), 2, 2025); err != nil || gotQuery != "member_id=2&year=2025" {
		t.Fatalf("指定年度時 query string 錯誤:%q %v", gotQuery, err)
	}

	failing := NewAPIClient(server.URL+"/missing", "secret", time.Second)
	if _, err := failing.DividendIncome(t.Context(), 0, 0); err == nil {
		t.Fatal("API 失敗時應回錯誤")
	}
}

// TestDividendIncomeTool 驗證預設值、年度範圍、錯誤分層與摘要內容。
func TestDividendIncomeTool(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC) }
	newToolset := func(f *fakeIncomeQuerier) *dividendIncomeToolset {
		return &dividendIncomeToolset{income: f, logf: func(string, ...any) {}, now: now}
	}

	f := &fakeIncomeQuerier{income: sampleIncome()}
	result, output, err := newToolset(f).dividendIncome(t.Context(), nil, DividendIncomeInput{})
	if err != nil {
		t.Fatalf("dividendIncome 不應失敗:%v", err)
	}
	if f.gotMember != 0 || f.gotYear != 0 {
		t.Fatalf("預設應為全部會員、今年,收到 %d/%d", f.gotMember, f.gotYear)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"全部會員的持股 2026 年股利入帳(截至 2026-11-03):全年現金約 6332 元,其中已入帳 4372 元、以目前持股推算 980 元,配股 14 股。去年全年 5000 元,去年同期 4100 元。",
		"- 1 月:現金 1500 元(已入帳)(2 筆);去年同月 1200 元",
		"- 6 月:現金 0 元(0 筆);去年同月 300 元",
		"- 11 月:現金 3852 元(已入帳 2872 元),配股 14 股(2 筆);去年同月 0 元",
		"- 12 月:現金 980 元(含推算 980 元)(1 筆)",
		"未扣二代健保",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if strings.Contains(summary, "- 2 月") {
		t.Errorf("沒有入帳的月份不應列出:%s", summary)
	}
	if out, ok := output.(DividendIncomeOutput); !ok || out.DataKind != "dividend_income" || len(out.Months) != 12 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).dividendIncome(t.Context(), nil, DividendIncomeInput{MemberID: 2, Year: 2027}); err != nil || f.gotMember != 2 || f.gotYear != 2027 {
		t.Fatalf("參數應原樣傳給 API:%v %d/%d", err, f.gotMember, f.gotYear)
	}
	for _, in := range []DividendIncomeInput{{MemberID: -1}, {Year: 1999}, {Year: 2028}} {
		if _, _, err := newToolset(f).dividendIncome(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	if _, _, err := newToolset(&fakeIncomeQuerier{err: errors.New("boom")}).dividendIncome(t.Context(), nil, DividendIncomeInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	// 沒注入時鐘時用現在時間,今年一定合法。
	plain := &dividendIncomeToolset{income: f, logf: func(string, ...any) {}}
	if _, _, err := plain.dividendIncome(t.Context(), nil, DividendIncomeInput{Year: time.Now().Year()}); err != nil {
		t.Fatalf("今年應合法:%v", err)
	}
}

// TestIncomeMonthStatus 涵蓋各種入帳狀態與指定會員、沒有配股的摘要。
func TestIncomeMonthStatus(t *testing.T) {
	for _, tc := range []struct {
		month DividendIncomeMonth
		want  string
	}{
		{DividendIncomeMonth{}, ""},
		{DividendIncomeMonth{CashAmount: 100, ReceivedCashAmount: 100}, "(已入帳)"},
		{DividendIncomeMonth{CashAmount: 100, ReceivedCashAmount: 40}, "(已入帳 40 元)"},
		{DividendIncomeMonth{CashAmount: 100, EstimatedCashAmount: 60}, "(含推算 60 元)"},
		{DividendIncomeMonth{CashAmount: 100}, "(待發放)"},
	} {
		if got := incomeMonthStatus(tc.month); got != tc.want {
			t.Errorf("incomeMonthStatus(%+v) = %q, want %q", tc.month, got, tc.want)
		}
	}
	member := dividendIncomeText(&DividendIncome{MemberID: 3, Year: 2025, AsOf: "2026-11-03"})
	if !strings.Contains(member, "會員 3的持股 2025 年股利入帳") || strings.Contains(member, "配股") {
		t.Fatalf("摘要錯誤:%s", member)
	}
}

// TestDividendIncomeToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestDividendIncomeToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeIncomeQuerier{}, nil)
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
		if tool.Name == "get_dividend_income" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_dividend_income 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_dividend_income")
	}
}
