// Package stock 測試 get_institutional_flow_ranking 的 API client、輸入驗證、摘要與註冊。
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

// fakeInstitutionalFlowQuerier 是同時具備基本 Querier 與 InstitutionalFlowQuerier 的假資料來源。
type fakeInstitutionalFlowQuerier struct {
	fakeQuerier
	envelope *InstitutionalFlowEnvelope
	err      error
	got      InstitutionalFlowOptions
}

func (f *fakeInstitutionalFlowQuerier) InstitutionalFlow(_ context.Context, opt InstitutionalFlowOptions) (*InstitutionalFlowEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleInstitutionalFlow 產生兩檔買超,其中一檔未分類、沒有占比與收盤價。
func sampleInstitutionalFlow() *InstitutionalFlowEnvelope {
	return &InstitutionalFlowEnvelope{
		DataAsOf: "2026-10-13", From: "2026-10-07", Days: 5, Investor: "trust", Side: "buy", Market: "all", Matched: 120,
		Items: []InstitutionalFlowItem{
			{Rank: 1, StockSymbol: "2330", Name: "台積電", Market: "twse", Industry: "半導體業", NetShares: 4607777, NetLots: 4608,
				NetAmount: 11571384050, VolumeRatio: ptr(3.79), BuyDays: 5, Streak: 10, ClosingPrice: ptr(1000.0)},
			{Rank: 2, StockSymbol: "6274", Name: "台燿", Market: "tpex", NetShares: 2578000, NetLots: 2578,
				NetAmount: 4474945000, BuyDays: 4, SellDays: 1, Streak: -1},
		},
	}
}

// TestAPIClientInstitutionalFlow 驗證 query string、欄位解析與 404／422／500 的分層。
func TestAPIClientInstitutionalFlow(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		switch gotQuery.Get("market") {
		case "twse":
			w.WriteHeader(http.StatusNotFound)
		case "tpex":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "none":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"data_as_of":"2026-10-13","from":"2026-10-13","days":1,"investor":"foreign","side":"buy",` +
				`"market":"all","industry":null,"matched":1,"items":[{"rank":1,"stock_symbol":"2603","name":"長榮","market":"twse",` +
				`"industry":"航運業","net_shares":15862704,"net_lots":15863,"net_amount":4187753856,"volume_ratio":null,` +
				`"buy_days":1,"sell_days":0,"streak":2,"closing_price":264}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	opt := InstitutionalFlowOptions{Days: 1, Investor: "foreign", Side: "buy", Market: "all", Limit: 30}
	envelope, err := client.InstitutionalFlow(t.Context(), opt)
	if err != nil || envelope.Industry != nil || envelope.Items[0].NetLots != 15863 || envelope.Items[0].VolumeRatio != nil || envelope.Items[0].Streak != 2 {
		t.Fatalf("InstitutionalFlow() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/market/institutional-flow" || gotQuery.Encode() != "days=1&investor=foreign&limit=30&market=all&side=buy" {
		t.Fatalf("未指定日期與產業不應帶參數:%s?%s", gotPath, gotQuery.Encode())
	}
	opt = InstitutionalFlowOptions{Date: "2026-10-01", Days: 5, Investor: "trust", Side: "sell", Market: "twse", Industry: "半導體業", Limit: 10}
	if _, err := client.InstitutionalFlow(t.Context(), opt); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if gotQuery.Get("date") != "2026-10-01" || gotQuery.Get("industry") != "半導體業" || gotQuery.Get("side") != "sell" {
		t.Fatalf("query string 錯誤:%s", gotQuery.Encode())
	}
	opt.Market = "tpex"
	if _, err := client.InstitutionalFlow(t.Context(), opt); !errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("422 應回 ErrIndustryNotFound:%v", err)
	}
	opt.Market = "none"
	if _, err := client.InstitutionalFlow(t.Context(), opt); err == nil || errors.Is(err, ErrMarketDataNotFound) || errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestInstitutionalFlowTool 驗證預設值、參數整理與檢查、錯誤分層、摘要與 structured output。
func TestInstitutionalFlowTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeInstitutionalFlowQuerier) *institutionalFlowToolset {
		return &institutionalFlowToolset{fq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeInstitutionalFlowQuerier{envelope: sampleInstitutionalFlow()}
	result, output, err := newToolset(f).institutionalFlow(t.Context(), nil, InstitutionalFlowInput{})
	if err != nil {
		t.Fatalf("institutionalFlow 不應失敗:%v", err)
	}
	if f.got != (InstitutionalFlowOptions{Days: 1, Investor: "foreign", Side: "buy", Market: "all", Limit: 30}) {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(InstitutionalFlowOutput); !ok || out.DataKind != "institutional_flow" || out.IsRealtime || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"投信買超排行(2026-10-07 至 2026-10-13 共 5 個交易日,all 市場)", "符合 120 檔",
		"第 1 名 2330 台積電(twse,半導體業):淨買超 +4608 張、約 115.71 億元,占期間成交量 3.79%;買超 5 天、賣超 0 天,連續買超 10 天;收盤 1000",
		"第 2 名 6274 台燿(tpex,未分類):淨買超 +2578 張、約 44.75 億元,占期間成交量 無;買超 4 天、賣超 1 天,連續賣超 1 天;收盤 無"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}

	in := InstitutionalFlowInput{Date: "2026-10-01", Days: 20, Investor: " TOTAL ", Side: " Sell ", Market: " TPEX ", Industry: " 半導體業 ", Limit: 100}
	if _, _, err := newToolset(f).institutionalFlow(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	want := InstitutionalFlowOptions{Date: "2026-10-01", Days: 20, Investor: "total", Side: "sell", Market: "tpex", Industry: "半導體業", Limit: 100}
	if f.got != want {
		t.Fatalf("參數應整理後傳給 API:%#v", f.got)
	}
	for _, bad := range []InstitutionalFlowInput{
		{Date: "2026-13-01"}, {Days: 61}, {Investor: "retail"}, {Side: "hold"}, {Market: "otc"},
		{Industry: strings.Repeat("產", 21)}, {Limit: 101},
	} {
		if _, _, err := newToolset(f).institutionalFlow(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeInstitutionalFlowQuerier{err: ErrMarketDataNotFound}).institutionalFlow(t.Context(), nil, InstitutionalFlowInput{}); err == nil || err.Error() != "指定日期之前查無籌碼資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeInstitutionalFlowQuerier{err: ErrIndustryNotFound}).institutionalFlow(t.Context(), nil, InstitutionalFlowInput{Industry: "不存在"}); err == nil || !strings.Contains(err.Error(), "找不到產業「不存在」") {
		t.Fatalf("422 應提示產業名稱:%v", err)
	}
	if _, _, err := newToolset(&fakeInstitutionalFlowQuerier{err: errors.New("boom")}).institutionalFlow(t.Context(), nil, InstitutionalFlowInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestInstitutionalFlowSummary 驗證賣超方向、產業條件與沒有股票的說明。
func TestInstitutionalFlowSummary(t *testing.T) {
	sell := institutionalFlowSummary(&InstitutionalFlowEnvelope{DataAsOf: "2026-10-13", From: "2026-10-13", Days: 1,
		Investor: "dealer", Side: "sell", Market: "twse", Industry: ptr("航運業")})
	for _, want := range []string{"自營商賣超排行", "twse 市場、航運業", "沒有符合條件的股票"} {
		if !strings.Contains(sell, want) {
			t.Errorf("賣超摘要缺少 %q:%s", want, sell)
		}
	}
}

// TestInstitutionalFlowToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestInstitutionalFlowToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeInstitutionalFlowQuerier{}, nil)
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
		if tool.Name == "get_institutional_flow_ranking" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_institutional_flow_ranking 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_institutional_flow_ranking")
	}
	schema := institutionalFlowSchema()
	if len(schema.Properties["investor"].Enum) != 4 || len(schema.Properties["side"].Enum) != 2 {
		t.Error("investor 應有 4 個列舉、side 應有 2 個列舉")
	}
}
