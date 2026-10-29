// Package stock 測試 get_market_forward_returns 的 API client、輸入驗證、摘要與註冊。
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

// fakeMarketForwardReturnsQuerier 是同時具備基本 Querier 與 MarketForwardReturnsQuerier 的假資料來源。
type fakeMarketForwardReturnsQuerier struct {
	fakeQuerier
	envelope *MarketForwardReturnsEnvelope
	err      error
	got      MarketForwardReturnsOptions
}

func (f *fakeMarketForwardReturnsQuerier) MarketForwardReturns(_ context.Context, opt MarketForwardReturnsOptions) (*MarketForwardReturnsEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleMarketForwardReturns 產生 5 日有樣本、60 日尚無樣本,並有兩個年度的大盤未來報酬。
func sampleMarketForwardReturns() *MarketForwardReturnsEnvelope {
	return &MarketForwardReturnsEnvelope{
		From: "2023-10-29", To: "2026-10-29", Years: 3, Market: "all", DataAsOf: "2026-10-22",
		Benchmark: []BenchmarkStat{
			{Days: 5, Samples: 2_500_000, AvgReturn: ptr(0.42), WinRate: ptr(0.4812)},
			{Days: 60, Samples: 0},
		},
		ByYear: []BenchmarkYear{
			{Year: 2024, Benchmark: []BenchmarkStat{{Days: 5, Samples: 800_000, AvgReturn: ptr(0.5), WinRate: ptr(0.5)}}},
			{Year: 2025, Benchmark: []BenchmarkStat{{Days: 5, Samples: 800_000, AvgReturn: ptr(-0.1), WinRate: ptr(0.45)}}},
		},
		ByMonth: []BenchmarkMonth{
			{Month: 5, Years: 3, Benchmark: []BenchmarkStat{
				{Days: 5, Samples: 200_000, AvgReturn: ptr(0.8), WinRate: ptr(0.55)},
				{Days: 20, Samples: 200_000, AvgReturn: ptr(3.18), WinRate: ptr(0.59)},
			}},
			{Month: 9, Years: 3, Benchmark: []BenchmarkStat{{Days: 20, Samples: 0}}},
		},
	}
}

// TestAPIClientMarketForwardReturns 驗證路徑、參數、欄位解析與 404／500 的分層。
func TestAPIClientMarketForwardReturns(t *testing.T) {
	var gotPath, gotRaw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRaw = r.URL.Path, r.URL.RawQuery
		switch r.URL.Query().Get("date") {
		case "1900-01-01":
			w.WriteHeader(http.StatusNotFound)
		case "2000-01-01":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"from":"2023-10-29","to":"2026-10-29","years":3,"market":"all","data_as_of":"2026-10-22",` +
				`"benchmark":[{"days":5,"samples":10,"avg_return":0.42,"win_rate":0.48},{"days":60,"samples":0,"avg_return":null,"win_rate":null}],` +
				`"by_year":null,"by_month":null}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.MarketForwardReturns(t.Context(), MarketForwardReturnsOptions{})
	if err != nil || len(envelope.Benchmark) != 2 || *envelope.Benchmark[0].AvgReturn != 0.42 ||
		envelope.Benchmark[1].AvgReturn != nil || envelope.ByYear == nil || envelope.ByMonth == nil || envelope.DataAsOf != "2026-10-22" {
		t.Fatalf("MarketForwardReturns() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/market/forward-returns" || gotRaw != "" {
		t.Fatalf("未指定條件不應帶參數:%s?%s", gotPath, gotRaw)
	}
	if _, err := client.MarketForwardReturns(t.Context(), MarketForwardReturnsOptions{Date: "2026-10-14", Years: 5, Market: "tpex"}); err != nil ||
		gotRaw != "date=2026-10-14&market=tpex&years=5" {
		t.Fatalf("參數錯誤:%s, %v", gotRaw, err)
	}
	if _, err := client.MarketForwardReturns(t.Context(), MarketForwardReturnsOptions{Date: "1900-01-01"}); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if _, err := client.MarketForwardReturns(t.Context(), MarketForwardReturnsOptions{Date: "2000-01-01"}); err == nil || errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestMarketForwardReturnsTool 驗證輸入驗證、結構化輸出、摘要與錯誤轉換。
func TestMarketForwardReturnsTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeMarketForwardReturnsQuerier) *marketForwardReturnsToolset {
		return &marketForwardReturnsToolset{fq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}
	f := &fakeMarketForwardReturnsQuerier{envelope: sampleMarketForwardReturns()}
	result, output, err := newToolset(f).marketForwardReturns(t.Context(), nil, MarketForwardReturnsInput{Date: "2026-10-29", Years: 3, Market: "TWSE"})
	if err != nil || f.got != (MarketForwardReturnsOptions{Date: "2026-10-29", Years: 3, Market: "twse"}) {
		t.Fatalf("marketForwardReturns 不應失敗:%v, %+v", err, f.got)
	}
	if out, ok := output.(MarketForwardReturnsOutput); !ok || out.DataKind != "market_forward_returns" || out.IsRealtime ||
		out.Disclaimer != AnalysisDisclaimer || len(out.ByYear) != 2 || len(out.ByMonth) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"all 市場 2023-10-29～2026-10-29(近 3 年,最後基準日 2026-10-22)",
		"整段:5 日 平均 0.42%、上漲比例 48.12%;60 日 尚無樣本。",
		"依基準日年度:2024 年 5 日 平均 0.5%、上漲比例 50%。2025 年 5 日 平均 -0.1%、上漲比例 45%。",
		"依基準日月份(跨年度合併,只列 20 日;",
		"5 月(3 年)平均 3.18%、上漲比例 59%;9 月(3 年)尚無樣本。",
		"不等於加權指數報酬",
		AnalysisDisclaimer,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	empty := sampleMarketForwardReturns()
	empty.ByYear = nil
	empty.ByMonth = nil
	if summary := marketForwardReturnsSummary(empty); strings.Contains(summary, "依基準日年度") || strings.Contains(summary, "依基準日月份") {
		t.Errorf("沒有年度與月份資料不應列出:%s", summary)
	}
	for _, bad := range []MarketForwardReturnsInput{{Date: "2026-13-01"}, {Years: 21}, {Years: -1}, {Market: "hk"}} {
		if _, _, err := newToolset(f).marketForwardReturns(t.Context(), nil, bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	if _, _, err := newToolset(&fakeMarketForwardReturnsQuerier{err: ErrMarketDataNotFound}).marketForwardReturns(t.Context(), nil, MarketForwardReturnsInput{}); err == nil || err.Error() != "指定期間查無未來報酬資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeMarketForwardReturnsQuerier{err: errors.New("boom")}).marketForwardReturns(t.Context(), nil, MarketForwardReturnsInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Errorf("內部錯誤應記錄一次:%v", logged)
	}
	if years := marketForwardReturnsSchema().Properties["years"]; years == nil || years.Default != nil {
		t.Error("years 不該有 schema 預設值")
	}
}

// TestMarketForwardReturnsToolIsRegisteredReadOnly 驗證只有具備 MarketForwardReturnsQuerier 時才註冊,且為唯讀。
func TestMarketForwardReturnsToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeMarketForwardReturnsQuerier{}, nil)
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
		if tool.Name == "get_market_forward_returns" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_market_forward_returns 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Fatal("具備 MarketForwardReturnsQuerier 時應註冊 get_market_forward_returns")
	}
}
