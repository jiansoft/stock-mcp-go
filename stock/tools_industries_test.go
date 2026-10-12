// Package stock 測試 get_industry_performance 的 API client、輸入驗證、摘要與註冊。
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

// fakeIndustriesQuerier 是同時具備基本 Querier 與 IndustriesQuerier 的假資料來源。
type fakeIndustriesQuerier struct {
	fakeQuerier
	envelope *IndustriesEnvelope
	err      error
	got      IndustriesOptions
}

func (f *fakeIndustriesQuerier) IndustryPerformance(_ context.Context, opt IndustriesOptions) (*IndustriesEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleIndustries 產生 n 個產業,第 i 個的加權漲跌幅為 n-i。
func sampleIndustries(n int) *IndustriesEnvelope {
	e := &IndustriesEnvelope{DataAsOf: "2026-10-12", Market: "all", Days: 1}
	for i := 0; i < n; i++ {
		e.Industries = append(e.Industries, IndustryPerformance{
			IndustryID: i + 1, IndustryName: fmt.Sprintf("產業%d", i+1), Stocks: 10, Advancers: 6, Decliners: 3, Unchanged: 1,
			CapWeightedChangePercent: ptr(float64(n - i)), MedianChangePercent: ptr(0.5), TradeValue: 1e9,
			TradeValueSharePercent: ptr(1.25), MedianPriceEarningRatio: ptr(15.2),
		})
	}
	return e
}

// TestAPIClientIndustryPerformance 驗證路徑、query string 與 404 語意。
func TestAPIClientIndustryPerformance(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		switch r.URL.Query().Get("market") {
		case "all":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"data_as_of":"2026-10-12","base_date":"2026-10-08","market":"all","days":1,"industries":[` +
				`{"industry_id":24,"industry_name":"半導體業","stocks":203,"advancers":120,"decliners":70,"unchanged":13,` +
				`"median_change_percent":0.8,"cap_weighted_change_percent":1.23,"trade_value":2.5e11,"trade_value_share_percent":41.5,` +
				`"median_price_earning_ratio":32.7,"median_price_to_book_ratio":null,` +
				`"top_gainer":{"stock_symbol":"2330","name":"台積電","change_percent":3.2},"top_loser":null}]}`))
		case "tpex":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.IndustryPerformance(t.Context(), IndustriesOptions{Market: "all", Days: 1})
	if err != nil || len(envelope.Industries) != 1 {
		t.Fatalf("IndustryPerformance() = %#v, %v", envelope, err)
	}
	semis := envelope.Industries[0]
	if *envelope.BaseDate != "2026-10-08" || semis.TopGainer.StockSymbol != "2330" || semis.TopLoser != nil || semis.MedianPriceToBookRatio != nil {
		t.Fatalf("欄位解析錯誤:%#v", semis)
	}
	if gotQuery != "days=1&market=all" {
		t.Fatalf("沒有日期時不應帶 date:%q", gotQuery)
	}
	if _, err := client.IndustryPerformance(t.Context(), IndustriesOptions{Market: "twse", Days: 5, Date: "1900-01-01"}); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if gotQuery != "date=1900-01-01&days=5&market=twse" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
	if envelope, err := client.IndustryPerformance(t.Context(), IndustriesOptions{Market: "tpex", Days: 1}); err == nil || errors.Is(err, ErrMarketDataNotFound) || envelope != nil {
		t.Fatalf("500 應回一般錯誤:%#v, %v", envelope, err)
	}
}

// TestIndustryPerformanceTool 驗證預設值、參數檢查、錯誤分層與 structured output。
func TestIndustryPerformanceTool(t *testing.T) {
	newToolset := func(f *fakeIndustriesQuerier) *industriesToolset {
		return &industriesToolset{industries: f, logf: func(string, ...any) {}}
	}

	f := &fakeIndustriesQuerier{envelope: sampleIndustries(3)}
	result, output, err := newToolset(f).industryPerformance(t.Context(), nil, IndustriesInput{})
	if err != nil {
		t.Fatalf("industryPerformance 不應失敗:%v", err)
	}
	if f.got != (IndustriesOptions{Market: "all", Days: 1}) {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(IndustriesOutput); !ok || out.DataKind != "industry_performance" || len(out.Industries) != 3 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "1. 產業1:加權 3%") {
		t.Fatalf("摘要內容錯誤:%s", text)
	}

	if _, _, err := newToolset(f).industryPerformance(t.Context(), nil, IndustriesInput{Market: "TPEX", Date: "2026-10-08", Days: 20}); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	if f.got != (IndustriesOptions{Market: "tpex", Date: "2026-10-08", Days: 20}) {
		t.Fatalf("參數應原樣傳給 API:%#v", f.got)
	}
	for _, in := range []IndustriesInput{{Market: "otc"}, {Date: "2026-13-01"}, {Days: 61}, {Limit: 51}} {
		if _, _, err := newToolset(f).industryPerformance(t.Context(), nil, in); err == nil {
			t.Errorf("%#v 應回錯誤", in)
		}
	}
	if _, _, err := newToolset(&fakeIndustriesQuerier{err: ErrMarketDataNotFound}).industryPerformance(t.Context(), nil, IndustriesInput{}); err == nil || !strings.Contains(err.Error(), "查無") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeIndustriesQuerier{err: errors.New("boom")}).industryPerformance(t.Context(), nil, IndustriesInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestIndustriesSummary 涵蓋全部列出、只列最強最弱、區間期初日與缺值顯示。
func TestIndustriesSummary(t *testing.T) {
	all := industriesSummary(sampleIndustries(4), 2)
	for _, want := range []string{
		"all 市場 2026-10-12 當日各產業表現,共 4 個產業",
		"1. 產業1:加權 4%、中位數 0.5%,漲 6 跌 3 平 1(共 10 檔),成交值占 1.25%,本益比中位數 15.2、股價淨值比中位數 無;領漲 無、領跌 無",
		"4. 產業4:加權 1%",
		"免責聲明",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("摘要缺少 %q:%s", want, all)
		}
	}
	if strings.Contains(all, "最強") {
		t.Errorf("產業數不超過 2×limit 時應全部列出:%s", all)
	}

	period := sampleIndustries(5)
	period.Days, period.BaseDate = 5, ptr("2026-10-01")
	period.Industries[0].IndustryName = ""
	period.Industries[0].TopGainer = &IndustryMover{StockSymbol: "2330", Name: "台積電", ChangePercent: 3.25}
	period.Industries[4].TopLoser = &IndustryMover{StockSymbol: "2303", Name: "聯電", ChangePercent: -2}
	summary := industriesSummary(period, 2)
	for _, want := range []string{
		"近 5 個交易日(2026-10-01 收盤起算)",
		"最強 2 個產業:\n1. 未分類:",
		"領漲 2330 台積電 3.25%",
		"最弱 2 個產業:\n4. 產業4",
		"5. 產業5:加權 1%",
		"領跌 2303 聯電 -2%",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if strings.Contains(summary, "3. 產業3") {
		t.Errorf("中間的產業不應列出:%s", summary)
	}
}

// TestIndustryPerformanceToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestIndustryPerformanceToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeIndustriesQuerier{}, nil)
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
		if tool.Name == "get_industry_performance" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_industry_performance 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_industry_performance")
	}
}
