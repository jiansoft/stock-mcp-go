// Package stock 測試 get_market_valuation 的 API client、輸入驗證、摘要與註冊,以及回測依估值排序與持股估值。
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

// fakeMarketValuationQuerier 是同時具備基本 Querier 與 MarketValuationQuerier 的假資料來源。
type fakeMarketValuationQuerier struct {
	fakeQuerier
	envelope *MarketValuationEnvelope
	err      error
	got      MarketValuationOptions
}

func (f *fakeMarketValuationQuerier) MarketValuation(_ context.Context, opt MarketValuationOptions) (*MarketValuationEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleMarketValuation 產生有本益比分布、沒有淨值比資料的估值水位。
func sampleMarketValuation() *MarketValuationEnvelope {
	return &MarketValuationEnvelope{
		DataAsOf: "2026-10-20", From: "2021-10-21", Years: 5, Market: "all",
		Current: MarketValuationPoint{Date: "2026-10-20", MedianPE: ptr(18.5), PESamples: 1234},
		PE:      &ValuationRatioStats{Current: 18.5, Percentile: 82.5, Min: 12.36, P25: 14.1, Median: 15.8, P75: 17.9, Max: 23.37, Days: 1214},
		Monthly: []MarketValuationPoint{
			{Date: "2026-08-31", MedianPE: ptr(17.2), MedianPB: ptr(1.9)},
			{Date: "2026-09-30", MedianPE: ptr(18.0)},
		},
	}
}

// TestAPIClientMarketValuation 驗證路徑、參數、欄位解析與 404／500 的分層。
func TestAPIClientMarketValuation(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"data_as_of":"2026-10-20","from":"2021-10-21","years":5,"market":"all",` +
				`"current":{"date":"2026-10-20","median_pe":18.5,"median_pb":null,"pe_samples":1234,"pb_samples":0},` +
				`"pe":{"current":18.5,"percentile":82.5,"min":12.36,"p25":14.1,"median":15.8,"p75":17.9,"max":23.37,"days":1214},` +
				`"pb":null,"monthly":null}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.MarketValuation(t.Context(), MarketValuationOptions{})
	if err != nil || envelope.PE == nil || envelope.PE.Percentile != 82.5 || envelope.PB != nil ||
		envelope.Current.MedianPB != nil || envelope.Current.PESamples != 1234 || envelope.Monthly == nil {
		t.Fatalf("MarketValuation() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/market/valuation" || gotRaw != "" {
		t.Fatalf("未指定條件不應帶參數:%s?%s", gotPath, gotRaw)
	}
	if _, err := client.MarketValuation(t.Context(), MarketValuationOptions{Date: "2026-10-14", Years: 3, Market: "tpex"}); err != nil ||
		gotRaw != "date=2026-10-14&market=tpex&years=3" {
		t.Fatalf("參數錯誤:%s, %v", gotRaw, err)
	}
	if _, err := client.MarketValuation(t.Context(), MarketValuationOptions{Date: "1900-01-01"}); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if _, err := client.MarketValuation(t.Context(), MarketValuationOptions{Date: "2000-01-01"}); err == nil || errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestMarketValuationTool 驗證輸入驗證、結構化輸出、摘要與錯誤轉換。
func TestMarketValuationTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeMarketValuationQuerier) *marketValuationToolset {
		return &marketValuationToolset{vq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}
	f := &fakeMarketValuationQuerier{envelope: sampleMarketValuation()}
	result, output, err := newToolset(f).marketValuation(t.Context(), nil, MarketValuationInput{Date: "2026-10-20", Years: 5, Market: "TWSE"})
	if err != nil || f.got != (MarketValuationOptions{Date: "2026-10-20", Years: 5, Market: "twse"}) {
		t.Fatalf("marketValuation 不應失敗:%v, %+v", err, f.got)
	}
	if out, ok := output.(MarketValuationOutput); !ok || out.DataKind != "market_valuation" || out.IsRealtime || out.PE.Days != 1214 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"all 市場估值水位(2021-10-21～2026-10-20,近 5 年",
		"本益比中位數 18.5 倍(1234 檔),在期間內第 82.5 百分位;期間最低 12.36、25% 14.1、中位數 15.8、75% 17.9、最高 23.37(1214 個交易日)。",
		"股價淨值比:期間內沒有資料。",
		"近 2 個月月底(本益比/淨值比):2026-08-31 17.2/1.9、2026-09-30 18/無。",
		"不等於加權指數本益比",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	for _, bad := range []MarketValuationInput{{Date: "2026-13-01"}, {Years: 11}, {Years: -1}, {Market: "hk"}} {
		if _, _, err := newToolset(f).marketValuation(t.Context(), nil, bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	if _, _, err := newToolset(&fakeMarketValuationQuerier{err: ErrMarketDataNotFound}).marketValuation(t.Context(), nil, MarketValuationInput{}); err == nil || err.Error() != "指定期間查無估值資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeMarketValuationQuerier{err: errors.New("boom")}).marketValuation(t.Context(), nil, MarketValuationInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Errorf("內部錯誤應記錄一次:%v", logged)
	}
	long := sampleMarketValuation()
	long.Monthly = make([]MarketValuationPoint, 30)
	for i := range long.Monthly {
		long.Monthly[i] = MarketValuationPoint{Date: "2026-01-31"}
	}
	if summary := marketValuationSummary(long); !strings.Contains(summary, "近 12 個月月底") {
		t.Errorf("月底走勢最多列 12 個月:%s", summary)
	}
	if years := marketValuationSchema().Properties["years"]; years == nil || years.Default != nil {
		t.Error("years 不該有 schema 預設值")
	}
}

// TestMarketValuationToolIsRegisteredReadOnly 驗證只有具備 MarketValuationQuerier 時才註冊,且為唯讀。
func TestMarketValuationToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeMarketValuationQuerier{}, nil)
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
		if tool.Name == "get_market_valuation" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_market_valuation 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Fatal("具備 MarketValuationQuerier 時應註冊 get_market_valuation")
	}
}

// TestBacktestRankByValuation 驗證回測 rank_by 可用 pe_ratio、pb_ratio,摘要寫「由低到高」。
func TestBacktestRankByValuation(t *testing.T) {
	for rankBy, want := range map[string]string{"pe_ratio": "換股當天本益比", "pb_ratio": "換股當天股價淨值比"} {
		opt, err := screenBacktestOptions(ScreenBacktestInput{RankBy: rankBy})
		if err != nil || opt.query().Get("rank_by") != rankBy {
			t.Fatalf("rank_by=%s:%v %v", rankBy, err, opt.query())
		}
		if rankByText(rankBy) != want || rankOrderText(rankBy) != "由低到高" {
			t.Errorf("rank_by=%s 的說明錯誤", rankBy)
		}
		if text := backtestOptionsText(opt); !strings.Contains(text, "取"+want+"最低的前 20 檔") {
			t.Errorf("穩健度摘要應寫最低:%s", text)
		}
	}
	if rankOrderText("rs_score") != "由高到低" {
		t.Error("相對強度應由高到低")
	}
	summary := screenBacktestSummary(&ScreenBacktestEnvelope{
		From: "2019-01-02", To: "2026-10-20", Market: "all", HoldDays: 20, Limit: 20, RankBy: ptr("pe_ratio"),
	})
	if !strings.Contains(summary, "依換股當天本益比由低到高取前 20 檔") {
		t.Fatalf("回測摘要排序方向錯誤:%s", summary)
	}
	enum := screenBacktestSchema().Properties["rank_by"].Enum
	if len(enum) != len(backtestRankBy) || enum[6] != "pe_ratio" || enum[7] != "pb_ratio" {
		t.Fatalf("rank_by enum 錯誤:%v", enum)
	}
}

// TestPortfolioValuationText 驗證持股摘要列出組合本益比與各檔本益比。
func TestPortfolioValuationText(t *testing.T) {
	summary := samplePortfolio()
	summary.WeightedPE = &WeightedMetric{Value: 16.2, CoveragePercent: 80}
	summary.Holdings = append(summary.Holdings, PortfolioHolding{StockSymbol: "2330", Name: "台積電", PERatio: ptr(25.3)})
	text := portfolioSummaryText(summary)
	for _, want := range []string{"組合本益比 16.2 倍(覆蓋 80% 市值);組合股價淨值比 無", "本益比 25.3,"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
}
