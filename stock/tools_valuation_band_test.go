// Package stock 測試 get_valuation_band 的 API client、輸入驗證、摘要與註冊。
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

// fakeBandQuerier 是同時具備基本 Querier 與 ValuationBandQuerier 的假資料來源。
type fakeBandQuerier struct {
	fakeQuerier
	envelope *ValuationBandEnvelope
	err      error
	gotYears int
}

func (f *fakeBandQuerier) ValuationBand(_ context.Context, _ string, years int) (*ValuationBandEnvelope, error) {
	f.gotYears = years
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleBand 是 2330 近 5 年的示意資料:本益比有目前值,股價淨值比目前無有效值。
func sampleBand() *ValuationBandEnvelope {
	return &ValuationBandEnvelope{
		StockSymbol: "2330", DataAsOf: ptr("2026-10-08"), Years: 5, From: ptr("2021-10-12"), ClosingPrice: ptr(1450.0),
		PriceEarningRatio: &RatioBand{
			Current: ptr(25.5), Percentile: ptr(62.3), Min: 12.1, P25: 16.71, Median: 23.37, P75: 27.62, Max: 31.4,
			Samples: 1210, ExcludedDays: 3, ImpliedPrices: &ImpliedPriceBand{P25: 950.3, Median: 1329.06, P75: 1570.75},
		},
		PriceToBookRatio: &RatioBand{Min: 3.1, P25: 4.2, Median: 5.0, P75: 5.8, Max: 7.9, Samples: 1076},
	}
}

// TestAPIClientValuationBand 驗證路徑、query string 與 404 語意。
func TestAPIClientValuationBand(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/2330/valuation-band":
			gotQuery = r.URL.RawQuery
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":"2026-10-08","years":3,"from":"2023-10-11","closing_price":1450,` +
				`"price_earning_ratio":{"current":25.5,"percentile":62.3,"min":12.1,"p25":16.71,"median":23.37,"p75":27.62,"max":31.4,"samples":1210,"excluded_days":3,` +
				`"implied_prices":{"p25":950.3,"median":1329.06,"p75":1570.75}},"price_to_book_ratio":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	band, err := client.ValuationBand(t.Context(), "2330", 3)
	if err != nil || band.PriceEarningRatio == nil || band.PriceToBookRatio != nil {
		t.Fatalf("ValuationBand() = %#v, %v", band, err)
	}
	if *band.PriceEarningRatio.Percentile != 62.3 || band.PriceEarningRatio.ImpliedPrices.Median != 1329.06 || band.Years != 3 {
		t.Fatalf("欄位解析錯誤:%#v", band.PriceEarningRatio)
	}
	if gotQuery != "years=3" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
	if _, err := client.ValuationBand(t.Context(), "9999", 5); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
}

// TestValuationBandTool 驗證預設年數、範圍檢查、錯誤分層與摘要內容。
func TestValuationBandTool(t *testing.T) {
	newToolset := func(f *fakeBandQuerier) *valuationBandToolset {
		return &valuationBandToolset{band: f, logf: func(string, ...any) {}}
	}

	f := &fakeBandQuerier{envelope: sampleBand()}
	result, output, err := newToolset(f).valuationBand(t.Context(), nil, ValuationBandInput{Symbol: "2330"})
	if err != nil {
		t.Fatalf("valuationBand 不應失敗:%v", err)
	}
	if f.gotYears != 5 {
		t.Fatalf("預設應為 5 年,收到 %d", f.gotYears)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"股票 2330 近 5 年(2021-10-12 至 2026-10-08)估值區間,最新收盤 1450 元。",
		"本益比:目前 25.5 倍,位於近期第 62.3 百分位;區間 12.1～31.4 倍,P25 16.71、中位數 23.37、P75 27.62(有效 1210 天,排除 3 天非正值)。",
		"回到 P25／中位數／P75 對應股價約 950.3／1329.06／1570.75 元。",
		"股價淨值比:目前無有效值(虧損或未提供);區間 3.1～7.9 倍,P25 4.2、中位數 5、P75 5.8(有效 1076 天)。",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(ValuationBandOutput); !ok || out.DataKind != "valuation_band" || out.Years != 5 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).valuationBand(t.Context(), nil, ValuationBandInput{Symbol: "2330", Years: 10}); err != nil || f.gotYears != 10 {
		t.Fatalf("years 10 應原樣傳給 API:%v %d", err, f.gotYears)
	}
	if _, _, err := newToolset(f).valuationBand(t.Context(), nil, ValuationBandInput{Symbol: "2330", Years: 11}); err == nil || !strings.Contains(err.Error(), "years") {
		t.Fatalf("years 超界應回錯誤:%v", err)
	}
	if _, _, err := newToolset(f).valuationBand(t.Context(), nil, ValuationBandInput{Symbol: " "}); err == nil {
		t.Fatal("空白代號應回錯誤")
	}
	if _, _, err := newToolset(&fakeBandQuerier{err: ErrStockNotFound}).valuationBand(t.Context(), nil, ValuationBandInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeBandQuerier{err: errors.New("boom")}).valuationBand(t.Context(), nil, ValuationBandInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestValuationBandSummaryEdgeCases 涵蓋沒有日線與倍數為 nil(ETF)。
func TestValuationBandSummaryEdgeCases(t *testing.T) {
	empty := valuationBandSummary(&ValuationBandEnvelope{StockSymbol: "6488", Years: 5})
	if !strings.Contains(empty, "股票 6488 沒有可分析的日線資料。") || !strings.Contains(empty, "免責聲明") {
		t.Fatalf("摘要錯誤:%s", empty)
	}

	etf := valuationBandSummary(&ValuationBandEnvelope{StockSymbol: "0050", DataAsOf: ptr("2026-10-08"), Years: 5, From: ptr("2021-10-12"), ClosingPrice: ptr(190.0)})
	for _, want := range []string{"本益比:期間內沒有有效數值(可能是 ETF 或長期虧損)。", "股價淨值比:期間內沒有有效數值(可能缺每股淨值)。"} {
		if !strings.Contains(etf, want) {
			t.Errorf("摘要缺少 %q:%s", want, etf)
		}
	}
}

// TestValuationBandToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestValuationBandToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeBandQuerier{}, nil)
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
		if tool.Name == "get_valuation_band" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_valuation_band 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_valuation_band")
	}
}
