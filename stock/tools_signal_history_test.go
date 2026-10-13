// Package stock 測試 get_signal_performance 與 get_stock_signal_history 的 API client、輸入驗證、摘要與註冊。
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

// fakeSignalHistoryQuerier 是同時具備基本 Querier 與 SignalHistoryQuerier 的假資料來源。
type fakeSignalHistoryQuerier struct {
	fakeQuerier
	performance *SignalPerformanceEnvelope
	stock       *StockSignalsEnvelope
	err         error
	gotPerf     SignalPerformanceOptions
	gotSymbol   string
	gotStock    StockSignalsOptions
}

func (f *fakeSignalHistoryQuerier) SignalPerformance(_ context.Context, opt SignalPerformanceOptions) (*SignalPerformanceEnvelope, error) {
	f.gotPerf = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.performance, nil
}

func (f *fakeSignalHistoryQuerier) StockSignals(_ context.Context, symbol string, opt StockSignalsOptions) (*StockSignalsEnvelope, error) {
	f.gotSymbol, f.gotStock = symbol, opt
	if f.err != nil {
		return nil, f.err
	}
	return f.stock, nil
}

// samplePerformance 產生一個有樣本與一個 60 日尚無樣本的訊號績效。
func samplePerformance() *SignalPerformanceEnvelope {
	return &SignalPerformanceEnvelope{
		From: "2025-10-14", To: "2026-10-13", Market: "all", Adjusted: true,
		Benchmark: []BenchmarkStat{
			{Days: 5, Samples: 100, AvgReturn: ptr(0.35), WinRate: ptr(0.5123)},
			{Days: 60, Samples: 0},
		},
		Items: []SignalPerformanceItem{{
			Indicator: "52_week", Kind: "new_high", Occurrences: 120,
			Horizons: []HorizonStat{
				{Days: 5, Samples: 118, AvgReturn: ptr(1.25), MedianReturn: ptr(0.8), WinRate: ptr(0.5678),
					AvgExcessReturn: ptr(0.9), BeatMarketRate: ptr(0.55)},
				{Days: 60, Samples: 0},
			},
		}},
	}
}

// sampleStockSignals 產生兩筆訊號、共 3 筆符合的個股訊號歷史。
func sampleStockSignals() *StockSignalsEnvelope {
	return &StockSignalsEnvelope{
		StockSymbol: "2330", From: "2026-04-17", To: "2026-10-13", Adjusted: true, Total: 3,
		Items: []StockSignalItem{
			{Date: "2026-10-13", Indicator: "kd", Kind: "golden_cross", Description: "K 值向上穿越 D 值",
				ClosingPrice: ptr(2540.0), ChangePercent: ptr(1.2)},
			{Date: "2026-09-01", Indicator: "close_ma60", Kind: "cross_below", Description: "收盤價跌破 60 日均線(季線)",
				ClosingPrice: ptr(2300.0), ChangePercent: ptr(-2.5), Return5d: ptr(-1.5), Return20d: ptr(4.25)},
		},
	}
}

// TestAPIClientSignalPerformance 驗證路徑、query string、404、422 與其他錯誤。
func TestAPIClientSignalPerformance(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		switch gotQuery.Get("industry") {
		case "":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"from":"2025-10-14","to":"2026-10-13","market":"all","kind":null,"indicator":"kd","industry":null,` +
				`"adjusted":true,"benchmark":[{"days":5,"samples":10,"avg_return":0.5,"win_rate":null}],` +
				`"items":[{"indicator":"kd","kind":"golden_cross","occurrences":3,"horizons":[{"days":5,"samples":3,` +
				`"avg_return":1.5,"median_return":null,"win_rate":0.6667,"avg_excess_return":1,"beat_market_rate":0.5}]}]}`))
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

	envelope, err := client.SignalPerformance(t.Context(), SignalPerformanceOptions{Market: "all", Indicator: "kd"})
	if err != nil || len(envelope.Items) != 1 || len(envelope.Benchmark) != 1 || !envelope.Adjusted {
		t.Fatalf("SignalPerformance() = %#v, %v", envelope, err)
	}
	h := envelope.Items[0].Horizons[0]
	if h.Samples != 3 || *h.AvgReturn != 1.5 || h.MedianReturn != nil || *h.WinRate != 0.6667 || *envelope.Indicator != "kd" {
		t.Fatalf("欄位解析錯誤:%#v", envelope)
	}
	if gotPath != "/api/v1/market/signals/performance" || gotQuery.Encode() != "indicator=kd&market=all" {
		t.Fatalf("空白條件不應帶入 query:%s?%s", gotPath, gotQuery.Encode())
	}

	if _, err := client.SignalPerformance(t.Context(), SignalPerformanceOptions{
		From: "2025-01-01", To: "2025-12-31", Market: "tpex", Kind: "new_high", Industry: "不存在",
	}); !errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("422 應回 ErrIndustryNotFound:%v", err)
	}
	if gotQuery.Get("from") != "2025-01-01" || gotQuery.Get("to") != "2025-12-31" || gotQuery.Get("kind") != "new_high" {
		t.Fatalf("query string 錯誤:%s", gotQuery.Encode())
	}
	if _, err := client.SignalPerformance(t.Context(), SignalPerformanceOptions{Market: "all", Industry: "沒資料"}); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if _, err := client.SignalPerformance(t.Context(), SignalPerformanceOptions{Market: "all", Industry: "故障"}); err == nil || errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestAPIClientStockSignals 驗證路徑跳脫、query string 與 404。
func TestAPIClientStockSignals(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.EscapedPath(), r.URL.Query()
		switch r.URL.Path {
		case "/api/v1/stocks/2330/signals":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","from":"2026-04-17","to":"2026-10-13","adjusted":true,"total":1,` +
				`"items":[{"date":"2026-10-13","indicator":"kd","kind":"golden_cross","description":"K 值向上穿越 D 值",` +
				`"closing_price":2540,"change_percent":null,"return_5d":1.5,"return_20d":null,"return_60d":null}]}`))
		case "/api/v1/stocks/9999/signals":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.StockSignals(t.Context(), "2330", StockSignalsOptions{Days: 180, Limit: 30})
	if err != nil || envelope.Total != 1 || *envelope.Items[0].Return5d != 1.5 || envelope.Items[0].Return20d != nil {
		t.Fatalf("StockSignals() = %#v, %v", envelope, err)
	}
	if gotQuery.Encode() != "days=180&limit=30" {
		t.Fatalf("空白條件不應帶入 query:%s", gotQuery.Encode())
	}
	if _, err := client.StockSignals(t.Context(), "2330", StockSignalsOptions{Date: "2026-10-13", Days: 30, Kind: "new_high", Indicator: "52_week", Limit: 5}); err != nil {
		t.Fatalf("帶條件不應失敗:%v", err)
	}
	if gotQuery.Get("date") != "2026-10-13" || gotQuery.Get("kind") != "new_high" || gotQuery.Get("indicator") != "52_week" {
		t.Fatalf("query string 錯誤:%s", gotQuery.Encode())
	}
	if _, err := client.StockSignals(t.Context(), "9999", StockSignalsOptions{Days: 1, Limit: 1}); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
	if _, err := client.StockSignals(t.Context(), "a/b", StockSignalsOptions{Days: 1, Limit: 1}); err == nil || errors.Is(err, ErrStockNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
	if gotPath != "/api/v1/stocks/a%2Fb/signals" {
		t.Fatalf("代號應跳脫:%s", gotPath)
	}
}

// TestSignalPerformanceTool 驗證預設值、參數檢查、錯誤分層與 structured output。
func TestSignalPerformanceTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeSignalHistoryQuerier) *signalHistoryToolset {
		return &signalHistoryToolset{history: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeSignalHistoryQuerier{performance: samplePerformance()}
	result, output, err := newToolset(f).signalPerformance(t.Context(), nil, SignalPerformanceInput{})
	if err != nil {
		t.Fatalf("signalPerformance 不應失敗:%v", err)
	}
	if f.gotPerf != (SignalPerformanceOptions{Market: "all"}) {
		t.Fatalf("預設條件錯誤:%#v", f.gotPerf)
	}
	if out, ok := output.(SignalPerformanceOutput); !ok || out.DataKind != "signal_performance" || out.IsRealtime || len(out.Items) != 1 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "52 週 新高(出現 120 次)") {
		t.Fatalf("摘要內容錯誤:%s", text)
	}

	in := SignalPerformanceInput{From: "2023-10-14", To: "2026-10-13", Market: " TPEX ", Kind: " new_high ", Indicator: "52_week", Industry: " 半導體業 "}
	if _, _, err := newToolset(f).signalPerformance(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	want := SignalPerformanceOptions{From: "2023-10-14", To: "2026-10-13", Market: "tpex", Kind: "new_high", Indicator: "52_week", Industry: "半導體業"}
	if f.gotPerf != want {
		t.Fatalf("參數應整理後傳給 API:%#v", f.gotPerf)
	}
	for _, bad := range []SignalPerformanceInput{
		{Market: "otc"}, {From: "2026-13-01"}, {To: "bad"}, {From: "2026-10-14", To: "2026-10-13"},
		{From: "2023-10-13", To: "2026-10-13"}, {Kind: "breakout"}, {Indicator: "ma1"},
		{Industry: strings.Repeat("業", 21)},
	} {
		if _, _, err := newToolset(f).signalPerformance(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeSignalHistoryQuerier{err: ErrMarketDataNotFound}).signalPerformance(t.Context(), nil, SignalPerformanceInput{}); err == nil || !strings.Contains(err.Error(), "查無技術訊號資料") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeSignalHistoryQuerier{err: ErrIndustryNotFound}).signalPerformance(t.Context(), nil, SignalPerformanceInput{Industry: "半導體"}); err == nil || !strings.Contains(err.Error(), "找不到產業「半導體」") {
		t.Fatalf("422 應提示產業名稱:%v", err)
	}
	if _, _, err := newToolset(&fakeSignalHistoryQuerier{err: errors.New("boom")}).signalPerformance(t.Context(), nil, SignalPerformanceInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestStockSignalHistoryTool 驗證預設值、參數檢查、錯誤分層與 structured output。
func TestStockSignalHistoryTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeSignalHistoryQuerier) *signalHistoryToolset {
		return &signalHistoryToolset{history: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeSignalHistoryQuerier{stock: sampleStockSignals()}
	result, output, err := newToolset(f).stockSignalHistory(t.Context(), nil, StockSignalHistoryInput{Symbol: " 2330 "})
	if err != nil {
		t.Fatalf("stockSignalHistory 不應失敗:%v", err)
	}
	if f.gotSymbol != "2330" || f.gotStock != (StockSignalsOptions{Days: 180, Limit: 30}) {
		t.Fatalf("預設條件錯誤:%q %#v", f.gotSymbol, f.gotStock)
	}
	if out, ok := output.(StockSignalHistoryOutput); !ok || out.DataKind != "stock_signal_history" || out.IsRealtime || out.Total != 3 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "共 3 筆") {
		t.Fatalf("摘要內容錯誤:%s", text)
	}

	in := StockSignalHistoryInput{Symbol: "2330", Date: "2026-10-13", Days: 730, Kind: "cross_below", Indicator: " close_ma60 ", Limit: 200}
	if _, _, err := newToolset(f).stockSignalHistory(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	if want := (StockSignalsOptions{Date: "2026-10-13", Days: 730, Kind: "cross_below", Indicator: "close_ma60", Limit: 200}); f.gotStock != want {
		t.Fatalf("參數應整理後傳給 API:%#v", f.gotStock)
	}
	for _, bad := range []StockSignalHistoryInput{
		{}, {Symbol: "2330", Date: "2026-02-30"}, {Symbol: "2330", Days: 731}, {Symbol: "2330", Kind: "x"},
		{Symbol: "2330", Indicator: "x"}, {Symbol: "2330", Limit: 201},
	} {
		if _, _, err := newToolset(f).stockSignalHistory(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeSignalHistoryQuerier{err: ErrStockNotFound}).stockSignalHistory(t.Context(), nil, StockSignalHistoryInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeSignalHistoryQuerier{err: errors.New("boom")}).stockSignalHistory(t.Context(), nil, StockSignalHistoryInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestSignalPerformanceSummary 涵蓋口徑、篩選、大盤基準、無樣本天期與無結果。
func TestSignalPerformanceSummary(t *testing.T) {
	e := samplePerformance()
	e.Industry, e.Indicator, e.Kind = ptr("半導體業"), ptr("52_week"), ptr("new_high")
	summary := signalPerformanceSummary(e)
	for _, want := range []string{
		"all 市場 2025-10-14～2026-10-13 技術訊號出現後的表現(報酬為訊號日收盤到之後第 N 個交易日收盤,依除權息、減資、分割還原,等同含息;",
		",篩選 產業「半導體業」、來源 52 週、種類 新高。",
		"大盤基準:5 日 平均 0.35%、上漲比例 51.23%;60 日 尚無樣本。",
		"- 52 週 新高(出現 120 次):5 日 平均 1.25%、中位數 0.8%、勝率 56.78%、超額 0.9%、勝過大盤 55%(樣本 118);60 日 尚無樣本",
		"過去表現不代表未來結果。",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}

	empty := signalPerformanceSummary(&SignalPerformanceEnvelope{From: "2026-01-01", To: "2026-01-31", Market: "tpex"})
	for _, want := range []string{"未還原除權息", "期間內沒有符合條件的訊號。"} {
		if !strings.Contains(empty, want) {
			t.Errorf("無結果摘要缺少 %q:%s", want, empty)
		}
	}
	if strings.Contains(empty, "大盤基準") || strings.Contains(empty, "篩選") {
		t.Errorf("無結果時不應列大盤基準與篩選:%s", empty)
	}
	if displayRate(nil) != "無" {
		t.Error("nil 比例應顯示無")
	}
}

// TestValidatePerformancePeriod 涵蓋缺一端不檢查、格式錯誤、先後顛倒與期間上限(含起訖 1,096 天)。
func TestValidatePerformancePeriod(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		ok       bool
	}{
		{"", "2026-10-13", true},
		{"2023-10-14", "2026-10-13", true},
		{"2023-10-13", "2026-10-13", false},
		{"2026-10-14", "2026-10-13", false},
		{"bad", "2026-10-13", false},
		{"2026-10-13", "bad", false},
	} {
		if err := validatePerformancePeriod(tc.from, tc.to); (err == nil) != tc.ok {
			t.Errorf("validatePerformancePeriod(%q, %q) = %v", tc.from, tc.to, err)
		}
	}
}

// TestStockSignalHistorySummary 涵蓋訊號列表、未滿天期、未列出筆數與無訊號。
func TestStockSignalHistorySummary(t *testing.T) {
	summary := stockSignalHistorySummary(sampleStockSignals())
	for _, want := range []string{
		"股票 2330 在 2026-04-17～2026-10-13 的技術訊號(之後報酬為訊號日收盤到之後第 N 個交易日收盤,依除權息、減資、分割還原,等同含息):共 3 筆。",
		"- 2026-10-13 KD 黃金交叉:收盤 2540 元、漲跌 1.2%;之後 5 日 未滿、20 日 未滿、60 日 未滿;K 值向上穿越 D 值",
		"- 2026-09-01 跌破季線:收盤 2300 元、漲跌 -2.5%;之後 5 日 -1.5%、20 日 4.25%、60 日 未滿",
		"另有 1 筆未列出",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}

	empty := stockSignalHistorySummary(&StockSignalsEnvelope{StockSymbol: "2330", From: "2026-10-13", To: "2026-10-13"})
	if !strings.Contains(empty, "未還原除權息):共 0 筆。\n期間內沒有出現技術訊號。") || strings.Contains(empty, "另有") {
		t.Errorf("無訊號摘要錯誤:%s", empty)
	}
}

// TestSignalHistoryToolsAreRegisteredReadOnly 經由 tools/list 驗證兩個工具有註冊且為唯讀,schema 列舉與 Data API 一致。
func TestSignalHistoryToolsAreRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeSignalHistoryQuerier{}, nil)
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
	found := map[string]bool{}
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if tool.Name == "get_signal_performance" || tool.Name == "get_stock_signal_history" {
			found[tool.Name] = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Errorf("%s 必須有 ReadOnlyHint=true", tool.Name)
			}
		}
	}
	if len(found) != 2 {
		t.Errorf("tools/list 缺少訊號歷史工具:%v", found)
	}
	for _, schema := range []map[string]bool{
		{"kind": len(signalPerformanceSchema().Properties["kind"].Enum) == 8, "indicator": len(signalPerformanceSchema().Properties["indicator"].Enum) == 9},
		{"kind": len(stockSignalHistorySchema().Properties["kind"].Enum) == 8, "required": len(stockSignalHistorySchema().Required) == 1},
	} {
		for name, ok := range schema {
			if !ok {
				t.Errorf("schema %s 錯誤", name)
			}
		}
	}
}
