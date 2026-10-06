package stock

// 測試 get_cagr_ranking、get_stock_cagr 的 API client、輸入驗證、摘要與註冊。

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

// fakeCagrQuerier 同時具備基本 Querier 與 CagrQuerier。
type fakeCagrQuerier struct {
	fakeQuerier
	ranking   *CagrRanking
	stock     *StockCagr
	err       error
	gotOpt    CagrRankingOptions
	gotMetric string
}

func (f *fakeCagrQuerier) CagrRanking(_ context.Context, opt CagrRankingOptions) (*CagrRanking, error) {
	f.gotOpt = opt
	return f.ranking, f.err
}

func (f *fakeCagrQuerier) StockCagr(_ context.Context, _ string, metric string) (*StockCagr, error) {
	f.gotMetric = metric
	return f.stock, f.err
}

func str(v string) *string { return &v }

func newCagrToolset(f *fakeCagrQuerier) *cagrToolset {
	return &cagrToolset{cagr: f, logf: func(string, ...any) {}}
}

// TestAPIClientCagr 驗證兩個 endpoint 的路徑、query string、404 語意與空陣列補齊。
func TestAPIClientCagr(t *testing.T) {
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/api/v1/market/cagr-ranking":
			gotQuery = r.URL.Query()
			if r.URL.Query().Get("period") == "M3" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"period":"Y5","metric":"total","sort":"cagr","date":"2026-10-05","principal":10000,"total":0,"coverage":{},"summary":{},"items":null}`))
		case "/api/v1/market/cagr-ranking/2330":
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","name":"台積電","metric":"price","date":"2026-10-05","principal":10000,"items":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	ranking, err := client.CagrRanking(t.Context(), CagrRankingOptions{Period: "Y5", Metric: "total", Market: "twse", IndustryID: 24, Limit: 10})
	if err != nil || ranking.Items == nil {
		t.Fatalf("CagrRanking() = %#v, %v", ranking, err)
	}
	if gotQuery.Get("stock_industry_id") != "24" || gotQuery.Get("market") != "twse" || gotQuery.Has("sort") || gotQuery.Get("include_incomplete") != "false" {
		t.Fatalf("query string 錯誤:%v", gotQuery)
	}
	if _, err := client.CagrRanking(t.Context(), CagrRankingOptions{Period: "M3"}); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("排行 404 應為 ErrMarketDataNotFound:%v", err)
	}
	stock, err := client.StockCagr(t.Context(), "2330", "price")
	if err != nil || stock.Items == nil || stock.Metric != "price" {
		t.Fatalf("StockCagr() = %#v, %v", stock, err)
	}
	if _, err := client.StockCagr(t.Context(), "9999", "total"); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("個股 404 應為 ErrStockNotFound:%v", err)
	}
}

// TestCagrRankingTool 驗證預設值、白名單、錯誤分層與摘要。
func TestCagrRankingTool(t *testing.T) {
	rank1 := int64(1)
	f := &fakeCagrQuerier{ranking: &CagrRanking{
		Period: "Y5", Metric: "total", Sort: "cagr", Date: "2026-10-05", BaseDate: str("2021-10-05"), Total: 2579,
		Coverage: CagrCoverage{Universe: 2579, Counted: 1901, SurvivorshipNote: true},
		Summary:  CagrSummary{PositiveRatio: "0.6796"},
		Items: []CagrRankingItem{
			{Rank: &rank1, StockSymbol: "6442", Name: "光聖", CagrPct: str("119.1586"), TotalReturnPct: str("4966.7098")},
			{StockSymbol: "9999", Name: "資料不足"},
		},
	}}
	result, output, err := newCagrToolset(f).cagrRanking(t.Context(), nil, CagrRankingInput{Period: "y5"})
	if err != nil {
		t.Fatalf("cagrRanking 不應失敗:%v", err)
	}
	if f.gotOpt.Period != "Y5" || f.gotOpt.Metric != "total" || f.gotOpt.Market != "all" || f.gotOpt.Limit != 20 || f.gotOpt.Sort != "" {
		t.Fatalf("預設值錯誤:%+v", f.gotOpt)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"Y5 期間", "期初 2021-10-05", "正報酬比例 0.6796", "1. 6442 光聖 年化 119.1586%", "存活者偏誤", "免責聲明"} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if strings.Contains(summary, "資料不足") {
		t.Errorf("沒有名次的項目不應出現在前幾名:%s", summary)
	}
	if out, ok := output.(CagrRankingOutput); !ok || out.DataKind != "cagr_ranking" {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	for _, in := range []CagrRankingInput{{Period: "Y4"}, {Metric: "net"}, {Sort: "eps"}, {Market: "otc"}, {Limit: 51}, {Offset: -1}, {IndustryID: -2}} {
		if _, _, err := newCagrToolset(f).cagrRanking(t.Context(), nil, in); err == nil {
			t.Errorf("輸入 %+v 應被拒絕", in)
		}
	}
	if _, _, err := newCagrToolset(&fakeCagrQuerier{err: ErrMarketDataNotFound}).cagrRanking(t.Context(), nil, CagrRankingInput{}); err == nil || !strings.Contains(err.Error(), "查無 CAGR") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newCagrToolset(&fakeCagrQuerier{err: errors.New("boom")}).cagrRanking(t.Context(), nil, CagrRankingInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩:%v", err)
	}
}

// TestStockCagrTool 驗證各期間摘要、資料不足與異常標示、404 訊息。
func TestStockCagrTool(t *testing.T) {
	f := &fakeCagrQuerier{stock: &StockCagr{
		StockSymbol: "2330", Name: str("台積電"), Metric: "total", Date: "2026-10-05",
		Items: []CagrPeriodItem{
			{Period: "Y1", CagrPct: str("85.1")},
			{Period: "Y5", CagrPct: str("31.2"), HasAnomaly: true},
			{Period: "Y10"},
		},
	}}
	result, _, err := newCagrToolset(f).stockCagr(t.Context(), nil, StockCagrInput{Symbol: "2330", Metric: "reinvested"})
	if err != nil || f.gotMetric != "reinvested" {
		t.Fatalf("stockCagr = %v, metric=%q", err, f.gotMetric)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"2330 台積電", "Y1 85.1%", "Y5 31.2%(有疑似除權或減資跳動)", "Y10 資料不足"} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if _, _, err := newCagrToolset(&fakeCagrQuerier{err: ErrStockNotFound}).stockCagr(t.Context(), nil, StockCagrInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號 9999") {
		t.Fatalf("404 訊息錯誤:%v", err)
	}
}

// TestCagrToolsAreRegisteredReadOnly 經由 tools/list 驗證兩個工具有註冊且為唯讀。
func TestCagrToolsAreRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeCagrQuerier{}, nil)
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
	wants := map[string]bool{"get_cagr_ranking": false, "get_stock_cagr": false}
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if _, ok := wants[tool.Name]; ok {
			wants[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
	}
	for name, ok := range wants {
		if !ok {
			t.Errorf("%s 未註冊或缺少 ReadOnlyHint", name)
		}
	}
}
