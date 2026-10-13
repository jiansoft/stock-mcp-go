// Package stock 測試 get_technical_indicators 的 API client、輸入驗證、摘要與註冊。
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

// fakeTechnicalsQuerier 是同時具備基本 Querier 與 TechnicalsQuerier 的假資料來源。
type fakeTechnicalsQuerier struct {
	fakeQuerier
	envelope *TechnicalsEnvelope
	err      error
	got      []any
}

func (f *fakeTechnicalsQuerier) TechnicalIndicators(_ context.Context, symbol, date string, days int) (*TechnicalsEnvelope, error) {
	f.got = []any{symbol, date, days}
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleTechnicals 產生 n 天、收盤 100 起逐日加 1 的指標;最後一天均線呈多頭排列。
func sampleTechnicals(n int) *TechnicalsEnvelope {
	e := &TechnicalsEnvelope{StockSymbol: "2330", DataAsOf: ptr("2026-10-12"), Days: n}
	for i := 0; i < n; i++ {
		e.Points = append(e.Points, TechnicalPoint{
			Date: fmt.Sprintf("2026-10-%02d", i+1), Close: float64(100 + i),
			MA5: ptr(110.0), MA20: ptr(105.0), MA60: ptr(100.0), Bias5: ptr(1.5),
			RSI14: ptr(65.2), K: ptr(81.3), D: ptr(75.0), MACDHistogram: ptr(0.42),
		})
	}
	return e
}

// TestAPIClientTechnicalIndicators 驗證路徑、query string 與 404 語意。
func TestAPIClientTechnicalIndicators(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		switch {
		case strings.Contains(r.URL.Path, "/2330/"):
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":"2026-10-12","days":1,"adjusted":false,` +
				`"points":[{"date":"2026-10-12","close":1450,"ma5":1440.2,"ma240":null,"k":81.3,"macd_histogram":-2.5}],` +
				`"signals":[{"indicator":"kd","kind":"overbought","description":"K 值 81.30,位於超買區(門檻 80)"}]}`))
		case strings.Contains(r.URL.Path, "/5000/"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.TechnicalIndicators(t.Context(), "2330", "", 1)
	if err != nil || len(envelope.Points) != 1 || len(envelope.Signals) != 1 {
		t.Fatalf("TechnicalIndicators() = %#v, %v", envelope, err)
	}
	point := envelope.Points[0]
	if *point.MA5 != 1440.2 || point.MA240 != nil || *point.MACDHistogram != -2.5 || envelope.Signals[0].Kind != "overbought" {
		t.Fatalf("欄位解析錯誤:%#v", envelope)
	}
	if gotPath != "/api/v1/stocks/2330/technicals" || gotQuery != "days=1" {
		t.Fatalf("沒有日期時不應帶 date:%s?%s", gotPath, gotQuery)
	}
	if _, err := client.TechnicalIndicators(t.Context(), "9999", "2026-10-08", 30); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
	if gotQuery != "date=2026-10-08&days=30" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
	if envelope, err := client.TechnicalIndicators(t.Context(), "5000", "", 20); err == nil || errors.Is(err, ErrStockNotFound) || envelope != nil {
		t.Fatalf("500 應回一般錯誤:%#v, %v", envelope, err)
	}
}

// TestTechnicalIndicatorsTool 驗證預設值、參數檢查、錯誤分層與 structured output。
func TestTechnicalIndicatorsTool(t *testing.T) {
	newToolset := func(f *fakeTechnicalsQuerier) *technicalsToolset {
		return &technicalsToolset{technicals: f, logf: func(string, ...any) {}}
	}

	f := &fakeTechnicalsQuerier{envelope: sampleTechnicals(3)}
	result, output, err := newToolset(f).technicalIndicators(t.Context(), nil, TechnicalsInput{Symbol: " 2330 "})
	if err != nil {
		t.Fatalf("technicalIndicators 不應失敗:%v", err)
	}
	if f.got[0] != "2330" || f.got[1] != "" || f.got[2] != 20 {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(TechnicalsOutput); !ok || out.DataKind != "technical_indicators" || len(out.Points) != 3 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "均線多頭排列") {
		t.Fatalf("摘要內容錯誤:%s", text)
	}

	if _, _, err := newToolset(f).technicalIndicators(t.Context(), nil, TechnicalsInput{Symbol: "2330", Date: "2026-10-08", Days: 120}); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	if f.got[1] != "2026-10-08" || f.got[2] != 120 {
		t.Fatalf("參數應原樣傳給 API:%#v", f.got)
	}
	for _, in := range []TechnicalsInput{{Symbol: ""}, {Symbol: "2330", Date: "2026-13-01"}, {Symbol: "2330", Days: 121}} {
		if _, _, err := newToolset(f).technicalIndicators(t.Context(), nil, in); err == nil {
			t.Errorf("%#v 應回錯誤", in)
		}
	}
	if _, _, err := newToolset(&fakeTechnicalsQuerier{err: ErrStockNotFound}).technicalIndicators(t.Context(), nil, TechnicalsInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeTechnicalsQuerier{err: errors.New("boom")}).technicalIndicators(t.Context(), nil, TechnicalsInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestTechnicalsSummary 涵蓋無資料、有訊號、無訊號與只列最近 10 日。
func TestTechnicalsSummary(t *testing.T) {
	empty := technicalsSummary(&TechnicalsEnvelope{StockSymbol: "2330"})
	if !strings.Contains(empty, "股票 2330 在指定日期之前沒有日線資料。") || !strings.Contains(empty, "免責聲明") {
		t.Fatalf("無資料摘要錯誤:%s", empty)
	}

	e := sampleTechnicals(12)
	e.Signals = []TechnicalSignal{{Indicator: "kd", Kind: "overbought", Description: "K 值 81.3,位於超買區(門檻 80)"}}
	summary := technicalsSummary(e)
	for _, want := range []string{
		"股票 2330 技術指標(資料日 2026-10-12,價格未還原除權息",
		"收盤 111 元;均線 MA5 110、MA10 無、MA20 105、MA60 100、MA120 無、MA240 無。",
		"乖離率:5 日 1.5%、20 日 無、60 日 無、240 日 無。",
		"RSI6 無、RSI12 無、RSI14 65.2;K 81.3、D 75;MACD DIF 無、訊號線 無、柱狀體 0.42。",
		"均線多頭排列",
		"最新一日訊號:\n- K 值 81.3,位於超買區(門檻 80)",
		"近 10 個交易日:\n- 2026-10-03 收 102、K 81.3、D 75、RSI14 65.2、MACD 柱 0.42",
		"- 2026-10-12 收 111",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if strings.Contains(summary, "2026-10-02 收") || strings.Contains(summary, "無%") {
		t.Errorf("只應列最近 10 日且缺值不接 %%:%s", summary)
	}

	short := sampleTechnicals(2)
	if text := technicalsSummary(short); !strings.Contains(text, "最新一日沒有交叉或超買超賣訊號。") || !strings.Contains(text, "近 2 個交易日:") {
		t.Errorf("無訊號摘要錯誤:%s", text)
	}
	short.Points[1].MA60 = nil
	if text := technicalsSummary(short); strings.Contains(text, "排列") || strings.Contains(text, "糾結") {
		t.Errorf("均線缺值時不應判斷排列:%s", text)
	}
}

// TestMAAlignment 涵蓋多頭、空頭、糾結與缺值。
func TestMAAlignment(t *testing.T) {
	cases := []struct {
		ma5, ma20, ma60 *float64
		want            string
	}{
		{ptr(3.0), ptr(2.0), ptr(1.0), "均線多頭排列(MA5 > MA20 > MA60)。"},
		{ptr(1.0), ptr(2.0), ptr(3.0), "均線空頭排列(MA5 < MA20 < MA60)。"},
		{ptr(2.0), ptr(1.0), ptr(3.0), "均線糾結(MA5、MA20、MA60 未呈單一方向排列)。"},
		{nil, ptr(1.0), ptr(1.0), ""},
		{ptr(1.0), nil, ptr(1.0), ""},
	}
	for _, c := range cases {
		if got := maAlignment(&TechnicalPoint{MA5: c.ma5, MA20: c.ma20, MA60: c.ma60}); got != c.want {
			t.Errorf("maAlignment(%v,%v,%v) = %q, want %q", c.ma5, c.ma20, c.ma60, got, c.want)
		}
	}
}

// TestTechnicalIndicatorsToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestTechnicalIndicatorsToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeTechnicalsQuerier{}, nil)
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
		if tool.Name == "get_technical_indicators" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_technical_indicators 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_technical_indicators")
	}
}
