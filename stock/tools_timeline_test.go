// Package stock 測試 get_stock_timeline 的 API client、輸入驗證、摘要與註冊。
package stock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeTimelineQuerier 是同時具備基本 Querier 與 StockTimelineQuerier 的假資料來源。
type fakeTimelineQuerier struct {
	fakeQuerier
	envelope *StockTimelineEnvelope
	err      error
	got      StockTimelineOptions
}

func (f *fakeTimelineQuerier) StockTimeline(_ context.Context, _ string, opt StockTimelineOptions) (*StockTimelineEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

func sampleTimeline() *StockTimelineEnvelope {
	return &StockTimelineEnvelope{
		StockSymbol: "2330", Name: ptr("台積電"), From: "2026-07-23", To: "2026-10-20", UpcomingTo: "2026-12-19", Total: 3,
		Counts: []TimelineKindCount{{Kind: "ex_dividend", Count: 1}, {Kind: "monthly_revenue", Count: 1}, {Kind: "chip_streak", Count: 1}},
		Events: []TimelineEvent{
			{Date: "2026-12-10", Kind: "ex_dividend", Title: "除息：現金股利 7 元", Detail: ptr("2026 年 Q2 股利"), Period: ptr("2026Q2"), Upcoming: true},
			{Date: "2026-10-13", Kind: "chip_streak", Title: "外資連賣 5 天", Detail: ptr("當天買賣超 -25641 張")},
		},
	}
}

// TestAPIClientStockTimeline 驗證路徑、query string 與 404 語意。
func TestAPIClientStockTimeline(t *testing.T) {
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stocks/2330/timeline" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.Query()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"stock_symbol":"2330","name":"台積電","from":"2026-07-23","to":"2026-10-20","upcoming_to":"2026-10-20","total":1,` +
			`"counts":[{"kind":"signal","count":1}],"events":[{"date":"2026-10-19","kind":"signal","title":"KD 超賣","detail":null,"period":null,"upcoming":false}]}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	zero := 0
	timeline, err := client.StockTimeline(t.Context(), "2330", StockTimelineOptions{
		Date: "2026-10-20", Days: 90, UpcomingDays: &zero, Kinds: []string{"signal", "insider"}, Limit: 50,
	})
	if err != nil || timeline.Total != 1 || timeline.Events[0].Kind != "signal" || *timeline.Name != "台積電" {
		t.Fatalf("StockTimeline() = %#v, %v", timeline, err)
	}
	for key, want := range map[string]string{"date": "2026-10-20", "days": "90", "upcoming_days": "0", "kinds": "signal,insider", "limit": "50"} {
		if gotQuery.Get(key) != want {
			t.Errorf("query %s = %q,應為 %q", key, gotQuery.Get(key), want)
		}
	}
	if _, err := client.StockTimeline(t.Context(), "2330", StockTimelineOptions{Days: 90, Limit: 100}); err != nil || gotQuery.Has("upcoming_days") || gotQuery.Has("kinds") || gotQuery.Has("date") {
		t.Fatalf("未指定的條件不應帶入 query:%v %v", gotQuery, err)
	}
	if _, err := client.StockTimeline(t.Context(), "9999", StockTimelineOptions{Days: 90, Limit: 100}); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
}

// TestStockTimelineTool 驗證預設值、範圍檢查、種類正規化、錯誤分層與摘要內容。
func TestStockTimelineTool(t *testing.T) {
	newToolset := func(f *fakeTimelineQuerier) *stockTimelineToolset {
		return &stockTimelineToolset{tq: f, logf: func(string, ...any) {}}
	}
	f := &fakeTimelineQuerier{envelope: sampleTimeline()}
	result, output, err := newToolset(f).stockTimeline(t.Context(), nil, StockTimelineInput{Symbol: "2330", Kinds: []string{" Signal ", "signal", "insider"}})
	if err != nil {
		t.Fatalf("stockTimeline 不應失敗:%v", err)
	}
	if f.got.Days != 90 || f.got.Limit != 100 || f.got.UpcomingDays != nil || strings.Join(f.got.Kinds, ",") != "signal,insider" {
		t.Fatalf("預設值或種類正規化錯誤:%#v", f.got)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"股票 2330 台積電 2026-07-23～2026-10-20 事件時間軸(另列至 2026-12-19 已公告的除權息、發放與減資):共 3 筆(除息 1、月營收 1、法人連續買賣超 1),列出最新 2 筆。",
		"- 2026-12-10 【已公告、尚未發生】除息：現金股利 7 元(2026 年 Q2 股利)",
		"- 2026-10-13 外資連賣 5 天(當天買賣超 -25641 張)",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(StockTimelineOutput); !ok || out.DataKind != "stock_timeline" {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	zero := 0
	if _, _, err := newToolset(f).stockTimeline(t.Context(), nil, StockTimelineInput{Symbol: "2330", UpcomingDays: &zero, Days: 730, Limit: 500}); err != nil || *f.got.UpcomingDays != 0 {
		t.Fatalf("upcoming_days 0 與邊界值應原樣傳給 API:%v %#v", err, f.got)
	}
	tooMany := 181
	for _, in := range []StockTimelineInput{
		{Symbol: "2330", Days: 731},
		{Symbol: "2330", Limit: 501},
		{Symbol: "2330", UpcomingDays: &tooMany},
		{Symbol: "2330", Kinds: []string{"earnings_call"}},
		{Symbol: "2330", Date: "2026-13-01"},
		{Symbol: " "},
	} {
		if _, _, err := newToolset(f).stockTimeline(t.Context(), nil, in); err == nil {
			t.Errorf("%#v 應回錯誤", in)
		}
	}
	if _, _, err := newToolset(&fakeTimelineQuerier{err: ErrStockNotFound}).stockTimeline(t.Context(), nil, StockTimelineInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeTimelineQuerier{err: errors.New("boom")}).stockTimeline(t.Context(), nil, StockTimelineInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestStockTimelineSummaryEmpty 涵蓋期間內沒有事件、沒有名稱與未知種類。
func TestStockTimelineSummaryEmpty(t *testing.T) {
	empty := stockTimelineSummary(&StockTimelineEnvelope{StockSymbol: "6488", From: "2026-07-23", To: "2026-10-20", UpcomingTo: "2026-10-20"})
	if !strings.Contains(empty, "股票 6488 2026-07-23～2026-10-20 事件時間軸:期間內沒有事件。") || strings.Contains(empty, "另列") {
		t.Fatalf("摘要錯誤:%s", empty)
	}
	unknown := stockTimelineSummary(&StockTimelineEnvelope{StockSymbol: "6488", From: "a", To: "b", UpcomingTo: "b", Total: 1,
		Counts: []TimelineKindCount{{Kind: "future_kind", Count: 1}}, Events: []TimelineEvent{{Date: "b", Kind: "future_kind", Title: "新事件"}}})
	if !strings.Contains(unknown, "共 1 筆(future_kind 1)") || !strings.Contains(unknown, "- b 新事件") {
		t.Fatalf("未知種類應原樣顯示:%s", unknown)
	}
}

// TestStockTimelineToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestStockTimelineToolIsRegisteredReadOnly(t *testing.T) {
	assertToolRegisteredReadOnly(t, &fakeTimelineQuerier{}, "get_stock_timeline")
}

// assertToolRegisteredReadOnly 以 tools/list 檢查工具有註冊且標示唯讀。
func assertToolRegisteredReadOnly(t *testing.T, q Querier, name string) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, q, nil)
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
			t.Fatalf("列出工具:%v", err)
		}
		if tool.Name == name {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Errorf("%s 必須有 ReadOnlyHint=true", name)
			}
			return
		}
	}
	t.Errorf("tools/list 缺少 %s", name)
}
