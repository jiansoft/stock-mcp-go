// Package stock 測試 get_industry_flow 的 API client、輸入驗證、摘要與註冊。
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

// fakeIndustryFlowQuerier 是同時具備基本 Querier 與 IndustryFlowQuerier 的假資料來源。
type fakeIndustryFlowQuerier struct {
	fakeQuerier
	envelope *IndustryFlowEnvelope
	err      error
	got      IndustryFlowOptions
}

func (f *fakeIndustryFlowQuerier) IndustryFlow(_ context.Context, opt IndustryFlowOptions) (*IndustryFlowEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleIndustryFlow 產生一個淨流入與一個淨流出(沒有成交金額)的產業。
func sampleIndustryFlow() *IndustryFlowEnvelope {
	return &IndustryFlowEnvelope{
		DataAsOf: "2026-10-13", From: "2026-10-07", Days: 5, Investor: "foreign", Market: "all", Order: "desc", TotalNetAmount: -1234567890,
		Items: []IndustryFlowItem{
			{Rank: 1, Industry: "半導體業", Stocks: 160, BuyStocks: 90, SellStocks: 70, NetAmount: 25_000_000_000, TradeValueRatio: ptr(4.5),
				TopBuyers:  []IndustryFlowStock{{StockSymbol: "2330", Name: "台積電", NetAmount: 11_571_384_050}, {StockSymbol: "3661", Name: "世芯-KY", NetAmount: 7_872_520_400}},
				TopSellers: []IndustryFlowStock{{StockSymbol: "2408", Name: "南亞科", NetAmount: -500_000_000}}},
			{Rank: 2, Industry: "航運業", Stocks: 30, BuyStocks: 5, SellStocks: 25, NetAmount: -3_000_000_000,
				TopBuyers: []IndustryFlowStock{}, TopSellers: []IndustryFlowStock{{StockSymbol: "2603", Name: "長榮", NetAmount: -2_000_000_000}}},
		},
	}
}

// TestAPIClientIndustryFlow 驗證 query string、欄位解析與 404／500 的分層。
func TestAPIClientIndustryFlow(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		switch gotQuery.Get("market") {
		case "twse":
			w.WriteHeader(http.StatusNotFound)
		case "tpex":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"data_as_of":"2026-10-13","from":"2026-10-07","days":5,"investor":"foreign","market":"all",` +
				`"order":"desc","total_net_amount":-20000,"items":[{"rank":1,"industry":"半導體業","stocks":2,"buy_stocks":2,` +
				`"sell_stocks":0,"net_amount":60000,"trade_value_ratio":null,"top_buyers":[{"stock_symbol":"2330","name":"台積電",` +
				`"net_amount":50000}],"top_sellers":[]}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.IndustryFlow(t.Context(), IndustryFlowOptions{Days: 5, Investor: "foreign", Market: "all", Order: "desc"})
	if err != nil || envelope.TotalNetAmount != -20000 || envelope.Items[0].TradeValueRatio != nil || envelope.Items[0].TopBuyers[0].NetAmount != 50000 {
		t.Fatalf("IndustryFlow() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/market/industry-flow" || gotQuery.Encode() != "days=5&investor=foreign&market=all&order=desc" {
		t.Fatalf("未指定日期不應帶 date:%s?%s", gotPath, gotQuery.Encode())
	}
	if _, err := client.IndustryFlow(t.Context(), IndustryFlowOptions{Date: "2026-10-01", Days: 1, Investor: "trust", Market: "twse", Order: "asc"}); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if gotQuery.Get("date") != "2026-10-01" || gotQuery.Get("investor") != "trust" || gotQuery.Get("order") != "asc" {
		t.Fatalf("query string 錯誤:%s", gotQuery.Encode())
	}
	if _, err := client.IndustryFlow(t.Context(), IndustryFlowOptions{Days: 5, Investor: "foreign", Market: "tpex", Order: "desc"}); err == nil || errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestIndustryFlowTool 驗證預設值、參數整理與檢查、錯誤分層、摘要與 structured output。
func TestIndustryFlowTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeIndustryFlowQuerier) *industryFlowToolset {
		return &industryFlowToolset{fq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeIndustryFlowQuerier{envelope: sampleIndustryFlow()}
	result, output, err := newToolset(f).industryFlow(t.Context(), nil, IndustryFlowInput{})
	if err != nil {
		t.Fatalf("industryFlow 不應失敗:%v", err)
	}
	if f.got != (IndustryFlowOptions{Days: 5, Investor: "foreign", Market: "all", Order: "desc"}) {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(IndustryFlowOutput); !ok || out.DataKind != "industry_flow" || out.IsRealtime || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"外資產業資金流向(2026-10-07 至 2026-10-13 共 5 個交易日,all 市場,淨流入最多在前)", "全體合計 -12.35 億元",
		"第 1 名 半導體業:淨額 +250 億元(占產業成交金額 4.5%);160 檔中買超 90、賣超 70;買超最多:2330 台積電 +115.71 億、3661 世芯-KY +78.73 億;賣超最多:2408 南亞科 -5 億",
		"第 2 名 航運業:淨額 -30 億元(占產業成交金額 無);30 檔中買超 5、賣超 25;買超最多:無;賣超最多:2603 長榮 -20 億"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}

	in := IndustryFlowInput{Date: "2026-10-01", Days: 60, Investor: " Trust ", Market: " TPEX ", Order: " ASC "}
	if _, _, err := newToolset(f).industryFlow(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	if f.got != (IndustryFlowOptions{Date: "2026-10-01", Days: 60, Investor: "trust", Market: "tpex", Order: "asc"}) {
		t.Fatalf("參數應整理後傳給 API:%#v", f.got)
	}
	for _, bad := range []IndustryFlowInput{{Date: "2026-13-01"}, {Days: 61}, {Investor: "retail"}, {Market: "otc"}, {Order: "up"}} {
		if _, _, err := newToolset(f).industryFlow(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeIndustryFlowQuerier{err: ErrMarketDataNotFound}).industryFlow(t.Context(), nil, IndustryFlowInput{}); err == nil || err.Error() != "指定日期之前查無籌碼資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeIndustryFlowQuerier{err: errors.New("boom")}).industryFlow(t.Context(), nil, IndustryFlowInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestIndustryFlowSummary 驗證 asc 方向與沒有產業的說明。
func TestIndustryFlowSummary(t *testing.T) {
	asc := industryFlowSummary(&IndustryFlowEnvelope{DataAsOf: "2026-10-13", From: "2026-10-13", Days: 1, Investor: "total", Market: "twse", Order: "asc"})
	for _, want := range []string{"三大法人合計產業資金流向", "淨流出最多在前", "全體合計 0 億元", "沒有產業資料"} {
		if !strings.Contains(asc, want) {
			t.Errorf("asc 摘要缺少 %q:%s", want, asc)
		}
	}
}

// TestIndustryFlowToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestIndustryFlowToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeIndustryFlowQuerier{}, nil)
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
		if tool.Name == "get_industry_flow" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_industry_flow 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_industry_flow")
	}
	if len(industryFlowSchema().Properties["order"].Enum) != 2 {
		t.Error("order 列舉應為 desc、asc")
	}
}
