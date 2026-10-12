// Package stock 測試 get_stock_peers 的 API client、輸入驗證、摘要與註冊。
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

// fakePeersQuerier 是同時具備基本 Querier 與 PeersQuerier 的假資料來源。
type fakePeersQuerier struct {
	fakeQuerier
	envelope *PeersEnvelope
	err      error
	gotLimit int
}

func (f *fakePeersQuerier) StockPeers(_ context.Context, _ string, limit int) (*PeersEnvelope, error) {
	f.gotLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// samplePeers 是 2330 在半導體業的示意資料。
func samplePeers() *PeersEnvelope {
	tsmc := PeerStock{
		StockSymbol: "2330", Name: "台積電", MarketID: 2, ClosingPrice: ptr(2540.0), MarketCap: ptr(65868219970180.0),
		PriceEarningRatio: ptr(29.44), PriceToBookRatio: ptr(10.24), DividendYieldPercent: ptr(0.28), FScore: ptr(9),
		GrossMarginPercent: ptr(67.72), RevenueYOYPercent: ptr(54.64),
	}
	mtk := PeerStock{StockSymbol: "2454", Name: "聯發科", MarketID: 2, MarketCap: ptr(7422905835125.0), PriceEarningRatio: ptr(76.38)}
	return &PeersEnvelope{
		StockSymbol: "2330", IndustryID: 24, IndustryName: "半導體業", DataAsOf: ptr("2026-10-12"), PeerCount: 150,
		Target: &tsmc,
		Metrics: []PeerMetric{
			{Metric: "market_cap", Better: "higher", Target: ptr(65868219970180.0), IndustryMedian: ptr(12345678900.0), Rank: ptr(1), Samples: 150},
			{Metric: "price_earning_ratio", Better: "lower", Target: ptr(29.44), IndustryMedian: ptr(35.1), Rank: ptr(48), Samples: 120},
			{Metric: "dividend_yield_percent", Better: "higher", Samples: 0},
			{Metric: "unknown_metric", Better: "higher", Samples: 3},
		},
		Peers: []PeerStock{tsmc, mtk},
	}
}

// TestAPIClientStockPeers 驗證路徑、query string 與 404 語意。
func TestAPIClientStockPeers(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/2330/peers":
			gotQuery = r.URL.RawQuery
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","industry_id":24,"industry_name":"半導體業","data_as_of":"2026-10-12","peer_count":2,` +
				`"target":{"stock_symbol":"2330","name":"台積電","market_id":2,"closing_price":2540,"market_cap":6.5e13,"price_earning_ratio":29.44,` +
				`"price_to_book_ratio":10.24,"dividend_yield_percent":null,"f_score":9,"gross_margin_percent":67.72,"revenue_yoy_percent":54.64},` +
				`"metrics":[{"metric":"price_earning_ratio","better":"lower","target":29.44,"industry_median":35.1,"rank":1,"samples":2}],"peers":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	peers, err := client.StockPeers(t.Context(), "2330", 5)
	if err != nil || peers.Target == nil || len(peers.Metrics) != 1 {
		t.Fatalf("StockPeers() = %#v, %v", peers, err)
	}
	if *peers.Target.FScore != 9 || peers.Target.DividendYieldPercent != nil || *peers.Metrics[0].Rank != 1 {
		t.Fatalf("欄位解析錯誤:%#v", peers)
	}
	if gotQuery != "limit=5" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
	if _, err := client.StockPeers(t.Context(), "9999", 20); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
}

// TestStockPeersTool 驗證預設筆數、範圍檢查、錯誤分層與摘要內容。
func TestStockPeersTool(t *testing.T) {
	newToolset := func(f *fakePeersQuerier) *peersToolset {
		return &peersToolset{peers: f, logf: func(string, ...any) {}}
	}

	f := &fakePeersQuerier{envelope: samplePeers()}
	result, output, err := newToolset(f).stockPeers(t.Context(), nil, PeersInput{Symbol: "2330"})
	if err != nil {
		t.Fatalf("stockPeers 不應失敗:%v", err)
	}
	if f.gotLimit != 20 {
		t.Fatalf("預設應為 20 檔,收到 %d", f.gotLimit)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"股票 2330 所屬產業「半導體業」共 150 檔同業(資料日 2026-10-12)。",
		"2330 台積電:市值 658682.2 億元、本益比 29.44、股價淨值比 10.24、殖利率 0.28%、F 分數 9、毛利率 67.72%、月營收年增 54.64%。",
		"- 市值(越高越好):目標 658682.2 億元,第 1／150 名,產業中位數 123.46 億元",
		"- 本益比(越低越好):目標 29.44 倍,第 48／120 名,產業中位數 35.1 倍",
		"- 殖利率(越高越好):目標 無%,無名次,產業中位數 無%",
		"- unknown_metric(越高越好)",
		"市值前 2 檔:",
		"- 2454 聯發科:市值 74229.06 億元、本益比 76.38、股價淨值比 無、殖利率 無%、F 分數 無",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(PeersOutput); !ok || out.DataKind != "stock_peers" || out.PeerCount != 150 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).stockPeers(t.Context(), nil, PeersInput{Symbol: "2330", Limit: 100}); err != nil || f.gotLimit != 100 {
		t.Fatalf("limit 100 應原樣傳給 API:%v %d", err, f.gotLimit)
	}
	if _, _, err := newToolset(f).stockPeers(t.Context(), nil, PeersInput{Symbol: "2330", Limit: 101}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit 超界應回錯誤:%v", err)
	}
	if _, _, err := newToolset(f).stockPeers(t.Context(), nil, PeersInput{Symbol: " "}); err == nil {
		t.Fatal("空白代號應回錯誤")
	}
	if _, _, err := newToolset(&fakePeersQuerier{err: ErrStockNotFound}).stockPeers(t.Context(), nil, PeersInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakePeersQuerier{err: errors.New("boom")}).stockPeers(t.Context(), nil, PeersInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestPeersSummaryWithoutTarget 涵蓋目標股沒有報價、未分類產業與沒有同業。
func TestPeersSummaryWithoutTarget(t *testing.T) {
	summary := peersSummary(&PeersEnvelope{StockSymbol: "0050", PeerCount: 0})
	for _, want := range []string{"股票 0050 所屬產業「未分類」共 0 檔同業(資料日 無)。", "0050 近 30 天沒有報價,沒有名次。"} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if strings.Contains(summary, "市值前") {
		t.Errorf("沒有同業不應列出市值前幾檔:%s", summary)
	}
}

// TestStockPeersToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestStockPeersToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakePeersQuerier{}, nil)
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
		if tool.Name == "get_stock_peers" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_stock_peers 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_stock_peers")
	}
}
