// Package stock 測試 get_market_regime 的 API client、輸入驗證、摘要與註冊。
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

// fakeMarketRegimeQuerier 是同時具備基本 Querier 與 MarketRegimeQuerier 的假資料來源。
type fakeMarketRegimeQuerier struct {
	fakeQuerier
	envelope *MarketRegimeEnvelope
	err      error
	got      string
}

func (f *fakeMarketRegimeQuerier) MarketRegime(_ context.Context, date string) (*MarketRegimeEnvelope, error) {
	f.got = date
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleMarketRegime 產生多頭排列、240 日均線資料不足的趨勢狀態。
func sampleMarketRegime() *MarketRegimeEnvelope {
	return &MarketRegimeEnvelope{
		DataAsOf: "2026-10-15", Close: 27012.5, Change: ptr(-120.3), ChangePercent: ptr(-0.44), Alignment: "bullish",
		MovingAverages: []MovingAverageStatus{
			{Days: 20, Value: ptr(26500.0), DistancePercent: ptr(1.93), Above: ptr(true), Rising: ptr(true)},
			{Days: 60, Value: ptr(27200.0), DistancePercent: ptr(-0.69), Above: ptr(false), Rising: ptr(false)},
			{Days: 240},
		},
		DaysOnSideOfMA60: ptr(3), High52Week: 28000, Low52Week: 18000, FromHighPercent: -3.53, FromLowPercent: 50.07,
		Return20d: ptr(4.2), Return60d: ptr(-1.5),
	}
}

// TestAPIClientMarketRegime 驗證路徑、date 參數、欄位解析與 404／500 的分層。
func TestAPIClientMarketRegime(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"data_as_of":"2026-10-15","close":27012.5,"change":-120.3,"change_percent":-0.44,` +
				`"alignment":"mixed","moving_averages":[{"days":5,"value":null,"distance_percent":null,"above":null,"rising":null}],` +
				`"days_on_side_of_ma60":null,"high_52_week":28000.0,"low_52_week":18000.0,"from_high_percent":-3.53,` +
				`"from_low_percent":50.07,"return_20d":4.2,"return_60d":null,"return_240d":null}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.MarketRegime(t.Context(), "")
	if err != nil || envelope.Close != 27012.5 || envelope.Alignment != "mixed" || envelope.MovingAverages[0].Value != nil ||
		envelope.DaysOnSideOfMA60 != nil || *envelope.Return20d != 4.2 {
		t.Fatalf("MarketRegime() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/market/regime" || gotRaw != "" {
		t.Fatalf("未指定日期不應帶參數:%s?%s", gotPath, gotRaw)
	}
	if _, err := client.MarketRegime(t.Context(), "2026-10-14"); err != nil || gotRaw != "date=2026-10-14" {
		t.Fatalf("date 參數錯誤:%s, %v", gotRaw, err)
	}
	if _, err := client.MarketRegime(t.Context(), "1900-01-01"); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if _, err := client.MarketRegime(t.Context(), "2000-01-01"); err == nil || errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestMarketRegimeTool 驗證日期驗證、結構化輸出與錯誤轉換。
func TestMarketRegimeTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeMarketRegimeQuerier) *marketRegimeToolset {
		return &marketRegimeToolset{rq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}
	f := &fakeMarketRegimeQuerier{envelope: sampleMarketRegime()}
	result, output, err := newToolset(f).marketRegime(t.Context(), nil, MarketRegimeInput{Date: "2026-10-15"})
	if err != nil || f.got != "2026-10-15" {
		t.Fatalf("marketRegime 不應失敗:%v, %q", err, f.got)
	}
	if out, ok := output.(MarketRegimeOutput); !ok || out.DataKind != "market_regime" || out.IsRealtime || out.Close != 27012.5 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "加權指數 2026-10-15 收盤 27012.50") {
		t.Fatalf("摘要內容錯誤:%s", text)
	}
	if _, _, err := newToolset(f).marketRegime(t.Context(), nil, MarketRegimeInput{Date: "2026-13-01"}); err == nil {
		t.Error("日期格式錯誤應回錯誤")
	}
	if _, _, err := newToolset(&fakeMarketRegimeQuerier{err: ErrMarketDataNotFound}).marketRegime(t.Context(), nil, MarketRegimeInput{}); err == nil || err.Error() != "指定日期之前查無加權指數資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeMarketRegimeQuerier{err: errors.New("boom")}).marketRegime(t.Context(), nil, MarketRegimeInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Errorf("內部錯誤應記錄一次:%v", logged)
	}
}

// TestMarketRegimeSummary 驗證收盤、排列、均線位置與方向、資料不足、52 週與近期漲跌。
func TestMarketRegimeSummary(t *testing.T) {
	summary := marketRegimeSummary(sampleMarketRegime())
	for _, want := range []string{
		"加權指數 2026-10-15 收盤 27012.50(漲跌 -120.3、-0.44%),多頭排列(收盤 > 20 > 60 > 120 > 240 日均線)。",
		"20 日 26500.00(收盤在上方 1.93%,均線上揚)",
		"60 日 27200.00(收盤在下方 -0.69%,均線下彎)",
		"240 日均線資料不足",
		"收盤已連續 3 個交易日站在 60 日均線同一側。",
		"52 週(250 個交易日)最高 28000.00、最低 18000.00;距高點 -3.53%、距低點 50.07%。",
		"近 20 日 4.2%、近 60 日 -1.5%、近 240 日 ",
		"趨勢狀態只描述價格位置",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	e := sampleMarketRegime()
	e.Alignment, e.DaysOnSideOfMA60 = "unknown", nil
	if other := marketRegimeSummary(e); !strings.Contains(other, ",unknown。") || strings.Contains(other, "站在 60 日均線") {
		t.Errorf("未知排列原樣顯示、沒有天數不列:%s", other)
	}
	if alignmentText("bearish") != alignmentLabels["bearish"] {
		t.Error("空頭排列說明錯誤")
	}
}

// TestMarketRegimeToolIsRegisteredReadOnly 驗證只有具備 MarketRegimeQuerier 時才註冊,且為唯讀。
func TestMarketRegimeToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeMarketRegimeQuerier{}, nil)
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
		if tool.Name == "get_market_regime" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_market_regime 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Fatal("具備 MarketRegimeQuerier 時應註冊 get_market_regime")
	}
}
