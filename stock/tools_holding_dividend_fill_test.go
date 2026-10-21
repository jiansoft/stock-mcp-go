// Package stock 測試 get_holding_dividend_fill 的 API client、輸入驗證、摘要與註冊。
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

// fakeHoldingDividendFillQuerier 是同時具備基本 Querier 與 HoldingDividendFillQuerier 的假資料來源。
type fakeHoldingDividendFillQuerier struct {
	fakeQuerier
	envelope *HoldingDividendFillEnvelope
	err      error
	got      int
}

func (f *fakeHoldingDividendFillQuerier) HoldingDividendFill(_ context.Context, days int) (*HoldingDividendFillEnvelope, error) {
	f.got = days
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

func sampleHoldingDividendFill() *HoldingDividendFillEnvelope {
	days := 6
	return &HoldingDividendFillEnvelope{
		Days:    365,
		Summary: DividendFillSummary{Events: 2, Filled: 1, AverageTradingDaysToFill: ptr(6.0), MedianTradingDaysToFill: ptr(6.0)},
		Items: []HoldingDividendFillItem{
			{StockSymbol: "0056", Name: "元大高股息", DividendFillEvent: DividendFillEvent{
				ExDate: "2026-09-15", CashDividend: ptr(1.0), StockDividend: ptr(0.0), PreExClose: ptr(40.0),
				LatestClose: ptr(36.0), GapPercent: ptr(10.0), TradingDaysSinceEx: 25,
			}},
			{StockSymbol: "2330", Name: "台積電", DividendFillEvent: DividendFillEvent{
				ExDate: "2026-07-10", CashDividend: ptr(5.0), Filled: true, TradingDaysToFill: &days,
			}},
		},
	}
}

// TestAPIClientHoldingDividendFill 驗證路徑、參數、JSON 展開欄位、空清單與錯誤。
func TestAPIClientHoldingDividendFill(t *testing.T) {
	var gotPath, gotRaw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRaw = r.URL.Path, r.URL.RawQuery
		if r.URL.Query().Get("days") == "999" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("content-type", "application/json")
		if r.URL.Query().Get("days") == "30" {
			_, _ = w.Write([]byte(`{"days":30,"summary":{"events":0,"filled":0,"average_trading_days_to_fill":null,"median_trading_days_to_fill":null},"items":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"days":365,"summary":{"events":1,"filled":0,"average_trading_days_to_fill":null,"median_trading_days_to_fill":null},` +
			`"items":[{"stock_symbol":"0056","name":"元大高股息","ex_date":"2026-09-15","cash_dividend":1,"stock_dividend":0,"pre_ex_date":"2026-09-12",` +
			`"pre_ex_close":40,"filled":false,"fill_date":null,"trading_days_to_fill":null,"trading_days_since_ex":25,"latest_close":36,"gap_percent":10}]}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.HoldingDividendFill(t.Context(), 0)
	if err != nil || len(envelope.Items) != 1 || envelope.Items[0].ExDate != "2026-09-15" || *envelope.Items[0].GapPercent != 10 || envelope.Items[0].Name != "元大高股息" {
		t.Fatalf("HoldingDividendFill() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/portfolio/dividend-fill" || gotRaw != "" {
		t.Fatalf("未指定天數不應帶參數:%s?%s", gotPath, gotRaw)
	}
	empty, err := client.HoldingDividendFill(t.Context(), 30)
	if err != nil || gotRaw != "days=30" || empty.Items == nil {
		t.Fatalf("沒有資料時 items 應是空清單:%#v, %v, %s", empty, err, gotRaw)
	}
	if _, err := client.HoldingDividendFill(t.Context(), 999); err == nil {
		t.Fatal("非 2xx 應回錯誤")
	}
}

// TestHoldingDividendFillTool 驗證輸入驗證、結構化輸出、摘要與錯誤轉換。
func TestHoldingDividendFillTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeHoldingDividendFillQuerier) *holdingDividendFillToolset {
		return &holdingDividendFillToolset{fq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}
	f := &fakeHoldingDividendFillQuerier{envelope: sampleHoldingDividendFill()}
	result, output, err := newToolset(f).holdingDividendFill(t.Context(), nil, HoldingDividendFillInput{Days: 180})
	if err != nil || f.got != 180 {
		t.Fatalf("holdingDividendFill 不應失敗:%v, %d", err, f.got)
	}
	if out, ok := output.(HoldingDividendFillOutput); !ok || out.DataKind != "holding_dividend_fill" || out.IsRealtime || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"持股近 365 天的除權息共 2 次,已填息 1 次(已填息者平均 6 個交易日、中位數 6)。",
		"尚未填息(1 次):0056 元大高股息 2026-09-15 除權息(現金 1 元)填息價 40、最新 36,還差 10%(已 25 個交易日)。",
		"已填息(1 次):2330 台積電 2026-07-10 除權息、第 6 個交易日填息。",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	for _, bad := range []int{29, 731, -1} {
		if _, _, err := newToolset(f).holdingDividendFill(t.Context(), nil, HoldingDividendFillInput{Days: bad}); err == nil {
			t.Errorf("days=%d 應回錯誤", bad)
		}
	}
	if _, _, err := newToolset(&fakeHoldingDividendFillQuerier{err: errors.New("boom")}).holdingDividendFill(t.Context(), nil, HoldingDividendFillInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Errorf("內部錯誤應記錄一次:%v", logged)
	}
	if days := holdingDividendFillSchema().Properties["days"]; days == nil || days.Default != nil {
		t.Error("days 不該有 schema 預設值")
	}
}

// TestHoldingDividendFillSummaryLimitsLists 驗證清單超過上限時只列前幾筆並註明省略數。
func TestHoldingDividendFillSummaryLimitsLists(t *testing.T) {
	e := &HoldingDividendFillEnvelope{Days: 365}
	for i := range holdingDividendFillListed + 2 {
		e.Items = append(e.Items, HoldingDividendFillItem{StockSymbol: fmt.Sprintf("%04d", i), DividendFillEvent: DividendFillEvent{ExDate: "2026-01-01"}})
	}
	summary := holdingDividendFillSummary(e)
	if !strings.Contains(summary, fmt.Sprintf("尚未填息(%d 次)", holdingDividendFillListed+2)) || !strings.Contains(summary, ";另有 2 次見結構化輸出") {
		t.Fatalf("清單應截斷並註明:%s", summary)
	}
	if strings.Contains(summary, "已填息(") {
		t.Fatal("沒有已填息時不應出現該段")
	}
	if intPtrText(nil) != "無" {
		t.Fatal("nil 應顯示無")
	}
}

// TestHoldingDividendFillToolIsRegisteredReadOnly 驗證只有具備介面時才註冊,且為唯讀。
func TestHoldingDividendFillToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeHoldingDividendFillQuerier{}, nil)
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
		if tool.Name == "get_holding_dividend_fill" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_holding_dividend_fill 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Fatal("具備 HoldingDividendFillQuerier 時應註冊 get_holding_dividend_fill")
	}
}
