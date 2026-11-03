// Package stock 測試 get_dividend_forecast_accuracy 的 API client、輸入驗證、摘要與註冊。
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

// fakeAccuracyQuerier 是同時具備基本 Querier 與 DividendForecastAccuracyQuerier 的假資料來源。
type fakeAccuracyQuerier struct {
	fakeQuerier
	accuracy  *DividendForecastAccuracy
	err       error
	gotMember int64
	gotDate   string
}

func (f *fakeAccuracyQuerier) DividendForecastAccuracy(_ context.Context, memberID int64, snapshotDate string) (*DividendForecastAccuracy, error) {
	f.gotMember, f.gotDate = memberID, snapshotDate
	if f.err != nil {
		return nil, f.err
	}
	return f.accuracy, nil
}

func sampleAccuracy() *DividendForecastAccuracy {
	return &DividendForecastAccuracy{
		SnapshotDate: ptr("2026-12-01"), AsOf: "2027-01-31", SettledForecast: 3100, SettledActual: 3072.4, PendingCount: 1,
		ByBasis: []DividendForecastBasisAccuracy{
			{Basis: "announced", Count: 1, MedianErrorPercent: ptr(2.52)},
			{Basis: "announced/cadence", Count: 1, MedianErrorPercent: ptr(0.0)},
		},
		Items: []DividendForecastAccuracyItem{
			{StockSymbol: "0056", Name: "元大高股息", Month: "2026-12", Basis: "announced", Forecast: 2800, Actual: ptr(2872.4), Difference: ptr(72.4), ErrorPercent: ptr(2.52), Status: "matched"},
			{StockSymbol: "00929", Name: "復華台灣科技優息", Month: "2026-12", Basis: "announced/cadence", Forecast: 100, Actual: ptr(100.0), Difference: ptr(0.0), ErrorPercent: ptr(0.0), Status: "matched"},
			{StockSymbol: "2884", Name: "玉山金", Month: "2027-01", Basis: "estimated", Forecast: 200, Actual: ptr(0.0), Difference: ptr(-200.0), Status: "missed"},
			{StockSymbol: "00878", Name: "國泰永續高股息", Month: "2027-01", Forecast: 0, Actual: ptr(100.0), Difference: ptr(100.0), Status: "unforecast"},
			{StockSymbol: "2330", Name: "台積電", Month: "2027-04", Basis: "latest_amount", Forecast: 500, Status: "pending"},
		},
	}
}

// TestAPIClientDividendForecastAccuracy 驗證路徑、快照日只在有值時送出,以及 null 陣列補齊。
func TestAPIClientDividendForecastAccuracy(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/portfolio/dividend-forecast/accuracy" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"member_id":0,"snapshot_date":null,"as_of":"2026-11-04","settled_forecast":0,"settled_actual":0,` +
			`"pending_count":0,"by_basis":null,"items":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	tests := []struct {
		date      string
		wantQuery string
	}{
		{"", "member_id=0"},
		{"2026-12-01", "member_id=0&snapshot_date=2026-12-01"},
	}
	for _, tt := range tests {
		accuracy, err := client.DividendForecastAccuracy(t.Context(), 0, tt.date)
		if err != nil || accuracy.Items == nil || accuracy.ByBasis == nil || accuracy.SnapshotDate != nil {
			t.Fatalf("DividendForecastAccuracy(%q) = %#v, %v", tt.date, accuracy, err)
		}
		if gotQuery != tt.wantQuery {
			t.Errorf("query string = %q,want %q", gotQuery, tt.wantQuery)
		}
	}
}

// TestAPIClientDividendForecastAccuracyError 確認上游錯誤原樣回傳給工具層。
func TestAPIClientDividendForecastAccuracyError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)
	if _, err := client.DividendForecastAccuracy(t.Context(), 1, ""); err == nil {
		t.Fatal("上游 500 應回錯誤")
	}
}

// TestDividendForecastAccuracyTool 驗證預設值、參數檢查、錯誤分層與摘要內容。
func TestDividendForecastAccuracyTool(t *testing.T) {
	newToolset := func(f *fakeAccuracyQuerier) *dividendForecastAccuracyToolset {
		return &dividendForecastAccuracyToolset{accuracy: f, logf: func(string, ...any) {}}
	}

	f := &fakeAccuracyQuerier{accuracy: sampleAccuracy()}
	result, output, err := newToolset(f).dividendForecastAccuracy(t.Context(), nil, DividendForecastAccuracyInput{})
	if err != nil {
		t.Fatalf("dividendForecastAccuracy 不應失敗:%v", err)
	}
	if f.gotMember != 0 || f.gotDate != "" {
		t.Fatalf("預設應為全部會員、最近一次快照,收到 %d/%q", f.gotMember, f.gotDate)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"全部會員 2026-12-01 快照的股利預估,比對到 2027-01-31:已結算預估 3100 元、實際入帳 3072 元;配對 2 筆、漏領 1 筆、未預估的入帳 1 筆、發放日未到 1 筆。",
		"- 已公告:1 筆,誤差中位數 2.52%",
		"- 已公告+依配息間隔推估:1 筆,誤差中位數 0%",
		"- 2884 玉山金 2027-01:預估 200 元、實際 0 元(依去年同期推估,發放月份已過仍未入帳)",
		"- 00878 國泰永續高股息 2027-01:預估 0 元、實際 100 元(快照沒有預估)",
		"(已公告,誤差 2.52%)",
		"發放日未到的不計入誤差",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if strings.Contains(summary, "2330") {
		t.Errorf("發放日未到的不應列在差額最大的幾筆:%s", summary)
	}
	if strings.Index(summary, "2884") > strings.Index(summary, "0056") {
		t.Errorf("差額大的應排前面:%s", summary)
	}
	if out, ok := output.(DividendForecastAccuracyOutput); !ok || out.DataKind != "dividend_forecast_accuracy" || len(out.Items) != 5 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).dividendForecastAccuracy(t.Context(), nil, DividendForecastAccuracyInput{MemberID: 2, SnapshotDate: "2026-12-01"}); err != nil || f.gotMember != 2 || f.gotDate != "2026-12-01" {
		t.Fatalf("參數應原樣傳給 API:%v %d/%q", err, f.gotMember, f.gotDate)
	}
	for _, in := range []DividendForecastAccuracyInput{{MemberID: -1}, {SnapshotDate: "2026-13-01"}, {SnapshotDate: "20261201"}} {
		if _, _, err := newToolset(f).dividendForecastAccuracy(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
	if _, _, err := newToolset(&fakeAccuracyQuerier{err: errors.New("boom")}).dividendForecastAccuracy(t.Context(), nil, DividendForecastAccuracyInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestDividendForecastAccuracyTextEdgeCases 涵蓋沒有快照、快照沒有資料、指定會員與筆數上限。
func TestDividendForecastAccuracyTextEdgeCases(t *testing.T) {
	none := dividendForecastAccuracyText(&DividendForecastAccuracy{MemberID: 3, AsOf: "2026-11-04"})
	if !strings.Contains(none, "會員 3還沒有股利預估快照") {
		t.Fatalf("沒有快照的摘要錯誤:%s", none)
	}
	empty := dividendForecastAccuracyText(&DividendForecastAccuracy{SnapshotDate: ptr("2026-12-01"), AsOf: "2026-12-05"})
	if !strings.Contains(empty, "全部會員在 2026-12-01 的快照沒有預估,快照後到 2026-12-05 也沒有現金股利入帳。") {
		t.Fatalf("空快照的摘要錯誤:%s", empty)
	}

	many := &DividendForecastAccuracy{SnapshotDate: ptr("2026-12-01"), AsOf: "2027-12-01"}
	for i := range 12 {
		diff := float64(i)
		many.Items = append(many.Items, DividendForecastAccuracyItem{StockSymbol: fmt.Sprintf("10%02d", i), Month: "2027-01", Basis: "estimated", Actual: ptr(diff), Difference: &diff, Status: "missed"})
	}
	got := dividendForecastAccuracyText(many)
	if !strings.Contains(got, "- 1011") || strings.Contains(got, "- 1003 ") {
		t.Errorf("應只列差額最大的 %d 筆:%s", maxAccuracyItemsInSummary, got)
	}
	if strings.Contains(got, "各預估依據") {
		t.Errorf("沒有配對成功的就不列依據彙總:%s", got)
	}
}

// TestAccuracyStatusText 確認每種比對結果都有中文說明。
func TestAccuracyStatusText(t *testing.T) {
	tests := []struct {
		item DividendForecastAccuracyItem
		want string
	}{
		{DividendForecastAccuracyItem{Status: "matched", Basis: "cadence", ErrorPercent: ptr(5.0)}, "依配息間隔推估,誤差 5%"},
		{DividendForecastAccuracyItem{Status: "missed", Basis: "announced"}, "已公告,發放月份已過仍未入帳"},
		{DividendForecastAccuracyItem{Status: "unforecast"}, "快照沒有預估"},
		{DividendForecastAccuracyItem{Status: "pending", Basis: "estimated"}, "發放日未到"},
	}
	for _, tt := range tests {
		if got := accuracyStatusText(tt.item); got != tt.want {
			t.Errorf("accuracyStatusText(%+v) = %q,want %q", tt.item, got, tt.want)
		}
	}
	if got := basisListText(""); got != "沒有預估" {
		t.Errorf("basisListText(\"\") = %q", got)
	}
	if got := absFloat(nil); got != 0 {
		t.Errorf("absFloat(nil) = %v", got)
	}
}

// TestDividendForecastAccuracyToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestDividendForecastAccuracyToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeAccuracyQuerier{}, nil)
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
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("tools/list:%v", err)
		}
		if tool.Name == "get_dividend_forecast_accuracy" {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatalf("工具應標為唯讀:%#v", tool.Annotations)
			}
			return
		}
	}
	t.Fatal("tools/list 找不到 get_dividend_forecast_accuracy")
}
