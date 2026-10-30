// Package stock 測試 get_signal_stocks 的 API client、輸入驗證、摘要與註冊。
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

// fakeSignalsQuerier 是同時具備基本 Querier 與 SignalsQuerier 的假資料來源。
type fakeSignalsQuerier struct {
	fakeQuerier
	envelope *SignalsEnvelope
	err      error
	got      SignalsOptions
}

func (f *fakeSignalsQuerier) SignalStocks(_ context.Context, opt SignalsOptions) (*SignalsEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleSignals 產生兩筆訊號、共 5 筆符合的回應。
func sampleSignals() *SignalsEnvelope {
	return &SignalsEnvelope{
		DataAsOf: "2026-10-12", Market: "all", Total: 5,
		Summary: []SignalCount{{Indicator: "kd", Kind: "golden_cross", Count: 3}, {Indicator: "52_week", Kind: "new_high", Count: 2}},
		Items: []SignalStock{
			{
				StockSymbol: "2330", Name: "台積電", Market: "twse", Industry: "半導體業",
				ClosingPrice: ptr(2540.0), ChangePercent: ptr(-0.39), TradeValue: ptr(45_678_000_000.0),
				Indicator: "kd", Kind: "golden_cross", Description: "K 值向上穿越 D 值,K=55.1(黃金交叉)",
			},
			{
				StockSymbol: "9103", Name: "美德醫療-DR", Market: "twse",
				Indicator: "close_ma60", Kind: "cross_above", Description: "收盤價站上 60 日均線(季線)",
			},
		},
	}
}

// TestAPIClientSignalStocks 驗證路徑、query string、404 與 422 語意。
func TestAPIClientSignalStocks(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		switch gotQuery.Get("industry") {
		case "":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"data_as_of":"2026-10-12","market":"all","kind":"new_high","indicator":null,"industry":null,` +
				`"adjusted":false,"total":1,"summary":[{"indicator":"52_week","kind":"new_high","count":1}],` +
				`"items":[{"stock_symbol":"2330","name":"台積電","market":"twse","industry":"半導體業","closing_price":2540,` +
				`"change_percent":null,"trade_value":1e9,"indicator":"52_week","kind":"new_high","description":"創 52 週新高"}]}`))
		case "不存在":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "沒資料":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.SignalStocks(t.Context(), SignalsOptions{Market: "all", Kind: "new_high", Limit: 30})
	if err != nil || envelope.Total != 1 || len(envelope.Items) != 1 || len(envelope.Summary) != 1 {
		t.Fatalf("SignalStocks() = %#v, %v", envelope, err)
	}
	item := envelope.Items[0]
	if *item.ClosingPrice != 2540 || item.ChangePercent != nil || *item.TradeValue != 1e9 || *envelope.Kind != "new_high" || envelope.Industry != nil {
		t.Fatalf("欄位解析錯誤:%#v", envelope)
	}
	if gotPath != "/api/v1/market/signals" || gotQuery.Encode() != "kind=new_high&limit=30&market=all" {
		t.Fatalf("空白條件不應帶入 query:%s?%s", gotPath, gotQuery.Encode())
	}

	if _, err := client.SignalStocks(t.Context(), SignalsOptions{
		Date: "2026-10-08", Market: "tpex", Indicator: "kd", Industry: "不存在", Limit: 5,
	}); !errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("422 應回 ErrIndustryNotFound:%v", err)
	}
	if gotQuery.Get("date") != "2026-10-08" || gotQuery.Get("indicator") != "kd" || gotQuery.Get("market") != "tpex" {
		t.Fatalf("query string 錯誤:%s", gotQuery.Encode())
	}
	if _, err := client.SignalStocks(t.Context(), SignalsOptions{Market: "all", Industry: "沒資料", Limit: 5}); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	_, err = client.SignalStocks(t.Context(), SignalsOptions{Market: "all", Industry: "故障", Limit: 5})
	var status *apiStatusError
	if err == nil || errors.Is(err, ErrIndustryNotFound) || !errors.As(err, &status) || err.Error() != "資料 API 回傳非成功狀態:500" {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestSignalStocksTool 驗證預設值、參數檢查、錯誤分層與 structured output。
func TestSignalStocksTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeSignalsQuerier) *signalsToolset {
		return &signalsToolset{signals: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeSignalsQuerier{envelope: sampleSignals()}
	result, output, err := newToolset(f).signalStocks(t.Context(), nil, SignalsInput{})
	if err != nil {
		t.Fatalf("signalStocks 不應失敗:%v", err)
	}
	if f.got != (SignalsOptions{Market: "all", Limit: 30}) {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(SignalsOutput); !ok || out.DataKind != "technical_signals" || out.IsRealtime || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "符合 5 筆") {
		t.Fatalf("摘要內容錯誤:%s", text)
	}

	in := SignalsInput{Date: "2026-10-08", Market: " TPEX ", Kind: " golden_cross ", Indicator: "kd", Industry: " 半導體業 ", Limit: 200}
	if _, _, err := newToolset(f).signalStocks(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	want := SignalsOptions{Date: "2026-10-08", Market: "tpex", Kind: "golden_cross", Indicator: "kd", Industry: "半導體業", Limit: 200}
	if f.got != want {
		t.Fatalf("參數應整理後傳給 API:%#v", f.got)
	}
	for _, bad := range []SignalsInput{
		{Market: "otc"}, {Date: "2026-13-01"}, {Kind: "breakdown"}, {Indicator: "ma1"},
		{Industry: strings.Repeat("業", 21)}, {Limit: 201},
	} {
		if _, _, err := newToolset(f).signalStocks(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeSignalsQuerier{err: ErrMarketDataNotFound}).signalStocks(t.Context(), nil, SignalsInput{}); err == nil || !strings.Contains(err.Error(), "查無技術訊號資料") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeSignalsQuerier{err: ErrIndustryNotFound}).signalStocks(t.Context(), nil, SignalsInput{Industry: "半導體"}); err == nil || !strings.Contains(err.Error(), "找不到產業「半導體」") {
		t.Fatalf("422 應提示產業名稱:%v", err)
	}
	if _, _, err := newToolset(&fakeSignalsQuerier{err: errors.New("boom")}).signalStocks(t.Context(), nil, SignalsInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestSignalsSummary 涵蓋篩選說明、家數、未列出筆數、缺值與無結果。
func TestSignalsSummary(t *testing.T) {
	e := sampleSignals()
	e.Industry, e.Indicator, e.Kind = ptr("半導體業"), ptr("kd"), ptr("golden_cross")
	summary := signalsSummary(e)
	for _, want := range []string{
		"all 市場 2026-10-12 技術訊號(收盤後計算,價格未還原除權息),篩選 產業「半導體業」、來源 KD、種類 黃金交叉:符合 5 筆。",
		"各訊號家數(不受種類與來源篩選影響):KD 黃金交叉 3、52 週 新高 2。",
		"- 2330 台積電(半導體業)KD 黃金交叉:收盤 2540 元、漲跌 -0.39%、成交值 456.78 億;K 值向上穿越 D 值",
		"- 9103 美德醫療-DR(未分類)站上季線:收盤 無 元、漲跌 無、成交值 無;收盤價站上 60 日均線(季線)",
		"另有 3 筆未列出",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}

	empty := signalsSummary(&SignalsEnvelope{DataAsOf: "2026-10-12", Market: "tpex"})
	if !strings.Contains(empty, "tpex 市場 2026-10-12 技術訊號(收盤後計算,價格未還原除權息):符合 0 筆。\n沒有符合條件的股票。") {
		t.Errorf("無結果摘要錯誤:%s", empty)
	}
	if strings.Contains(empty, "各訊號家數") || strings.Contains(empty, "另有") {
		t.Errorf("無結果時不應列家數與未列出筆數:%s", empty)
	}

	adjusted := signalsSummary(&SignalsEnvelope{DataAsOf: "2026-10-13", Market: "all", Adjusted: true})
	if !strings.Contains(adjusted, "all 市場 2026-10-13 技術訊號(收盤後計算,價格已還原權值):符合 0 筆。") {
		t.Errorf("還原權值的口徑說明錯誤:%s", adjusted)
	}
}

// TestSignalLabel 涵蓋交叉、站上跌破、52 週、相對強度穿越、區間突破與假突破、未知名稱。
func TestSignalLabel(t *testing.T) {
	cases := map[[2]string]string{
		{"ma5_ma20", "golden_cross"}:   "MA5/MA20 黃金交叉",
		{"close_ma240", "cross_below"}: "跌破年線",
		{"rsi14", "oversold"}:          "RSI14 超賣",
		{"52_week", "new_low"}:         "52 週 新低",
		{"rs80", "cross_above"}:        "相對強度站上 80 分",
		{"rs50", "cross_below"}:        "相對強度跌破 50 分",
		{"range_breakout", "breakout"}: "區間突破",
		{"range_breakout", "failed"}:   "區間假突破",
		{"unknown", "golden_cross"}:    "unknown golden_cross",
		{"kd", "unknown"}:              "kd unknown",
	}
	for in, want := range cases {
		if got := signalLabel(in[0], in[1]); got != want {
			t.Errorf("signalLabel(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	if len(signalsSchema().Properties["kind"].Enum) != 10 || len(signalsSchema().Properties["indicator"].Enum) != 12 {
		t.Error("schema 列舉數量應與 Data API 一致")
	}
}

// TestSignalStocksToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestSignalStocksToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeSignalsQuerier{}, nil)
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
		if tool.Name == "get_signal_stocks" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_signal_stocks 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_signal_stocks")
	}
}
