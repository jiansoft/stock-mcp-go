// Package stock 測試 get_dividend_forecast 的 API client、輸入驗證、摘要與註冊。
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

// fakeForecastQuerier 是同時具備基本 Querier 與 DividendForecastQuerier 的假資料來源。
type fakeForecastQuerier struct {
	fakeQuerier
	forecast  *DividendForecast
	err       error
	gotMember int64
	gotMonths int
}

func (f *fakeForecastQuerier) DividendForecast(_ context.Context, memberID int64, months int) (*DividendForecast, error) {
	f.gotMember, f.gotMonths = memberID, months
	if f.err != nil {
		return nil, f.err
	}
	return f.forecast, nil
}

func sampleForecast() *DividendForecast {
	return &DividendForecast{
		From: "2026-10-12", To: "2027-10-12", Total: 3936, AnnouncedTotal: 1720, EstimatedTotal: 2216, ForwardYieldPercent: ptr(9.84),
		Months: []DividendForecastMonth{
			{Month: "2026-10"},
			{Month: "2026-11", Announced: 1720},
			{Month: "2027-02", Estimated: 866},
			{Month: "2027-08", Announced: 100, Estimated: 1350},
		},
		Items: []DividendForecastItem{
			{StockSymbol: "0056", Name: "元大高股息", Shares: 1000, ExDate: "2026-10-22", PayDate: "2026-11-11", CashPerShare: 1.72, Amount: 1720, Basis: "announced"},
			{StockSymbol: "0056", Name: "元大高股息", Shares: 1000, ExDate: "2027-01-22", PayDate: "2027-02-11", CashPerShare: 0.866, Amount: 866, Basis: "estimated"},
			{StockSymbol: "2887", Name: "台新新光金", Shares: 200, ExDate: "2027-07-24", PayDate: "2027-08-23", CashPerShare: 0.5, Amount: 100, Basis: "amount_announced"},
		},
	}
}

// TestAPIClientDividendForecast 驗證路徑、query string 與 null 陣列補齊。
func TestAPIClientDividendForecast(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/portfolio/dividend-forecast" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"member_id":0,"from":"2026-10-12","to":"2027-04-12","total":0,"announced_total":0,"estimated_total":0,` +
			`"forward_yield_percent":null,"months":null,"items":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	forecast, err := client.DividendForecast(t.Context(), 0, 6)
	if err != nil || forecast.Items == nil || forecast.Months == nil || forecast.ForwardYieldPercent != nil {
		t.Fatalf("DividendForecast() = %#v, %v", forecast, err)
	}
	if gotQuery != "member_id=0&months=6" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
}

// TestDividendForecastTool 驗證預設值、範圍檢查、錯誤分層與摘要內容。
func TestDividendForecastTool(t *testing.T) {
	newToolset := func(f *fakeForecastQuerier) *dividendForecastToolset {
		return &dividendForecastToolset{forecast: f, logf: func(string, ...any) {}}
	}

	f := &fakeForecastQuerier{forecast: sampleForecast()}
	result, output, err := newToolset(f).dividendForecast(t.Context(), nil, DividendForecastInput{})
	if err != nil {
		t.Fatalf("dividendForecast 不應失敗:%v", err)
	}
	if f.gotMember != 0 || f.gotMonths != 12 {
		t.Fatalf("預設應為全部會員、12 個月,收到 %d/%d", f.gotMember, f.gotMonths)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"全部會員預估 2026-10-12 之後到 2027-10-12 可收到現金股利約 3936 元(已公告 1720 元、依去年同期推估 2216 元),預估殖利率 9.84%。",
		"- 2026-11:1720 元",
		"- 2027-02:866 元(推估)",
		"- 2027-08:1450 元(其中推估 1350 元)",
		"- 0056 元大高股息 2026-11-11 發放 1720 元(1000 股 × 1.72 元,已公告)",
		"(200 股 × 0.5 元,金額已公告、日期推估)",
		"未扣二代健保",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if strings.Contains(summary, "2026-10:") {
		t.Errorf("沒有入帳的月份不應列出:%s", summary)
	}
	if out, ok := output.(DividendForecastOutput); !ok || out.DataKind != "dividend_forecast" || len(out.Items) != 3 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).dividendForecast(t.Context(), nil, DividendForecastInput{MemberID: 1, Months: 24}); err != nil || f.gotMember != 1 || f.gotMonths != 24 {
		t.Fatalf("參數應原樣傳給 API:%v %d/%d", err, f.gotMember, f.gotMonths)
	}
	for _, in := range []DividendForecastInput{{MemberID: -1}, {Months: 25}} {
		if _, _, err := newToolset(f).dividendForecast(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	if _, _, err := newToolset(&fakeForecastQuerier{err: errors.New("boom")}).dividendForecast(t.Context(), nil, DividendForecastInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestDividendForecastTextEdgeCases 涵蓋沒有配息、指定會員與最大筆數上限。
func TestDividendForecastTextEdgeCases(t *testing.T) {
	empty := dividendForecastText(&DividendForecast{MemberID: 3, From: "2026-10-12", To: "2027-10-12"})
	if !strings.Contains(empty, "會員 3在 2026-10-12 之後到 2027-10-12 預估沒有現金股利入帳") {
		t.Fatalf("摘要錯誤:%s", empty)
	}

	f := sampleForecast()
	f.Items = nil
	for i := range 12 {
		f.Items = append(f.Items, DividendForecastItem{StockSymbol: fmt.Sprintf("10%02d", i), Amount: float64(i), Basis: "estimated"})
	}
	got := dividendForecastText(f)
	if !strings.Contains(got, "- 1011") || strings.Contains(got, "- 1003 ") {
		t.Errorf("應只列金額最大的 %d 筆:%s", maxForecastItemsInSummary, got)
	}
	if !strings.Contains(got, "依去年同期推估)") {
		t.Errorf("推估依據應轉成中文:%s", got)
	}
}

// TestDividendForecastToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestDividendForecastToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeForecastQuerier{}, nil)
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
		if tool.Name == "get_dividend_forecast" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_dividend_forecast 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_dividend_forecast")
	}
}
