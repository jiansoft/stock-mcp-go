// Package stock 測試 get_industry_strength 的 API client、輸入驗證、摘要與註冊。
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

// fakeIndustryStrengthQuerier 是同時具備基本 Querier 與 IndustryStrengthQuerier 的假資料來源。
type fakeIndustryStrengthQuerier struct {
	fakeQuerier
	envelope *IndustryStrengthEnvelope
	err      error
	got      IndustryStrengthOptions
}

func (f *fakeIndustryStrengthQuerier) IndustryStrength(_ context.Context, opt IndustryStrengthOptions) (*IndustryStrengthEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleIndustryStrength 產生兩個產業,其中一個沒有前期資料。
func sampleIndustryStrength() *IndustryStrengthEnvelope {
	return &IndustryStrengthEnvelope{
		DataAsOf: "2026-10-13", PriorAsOf: ptr("2026-09-14"), Market: "all", Order: "desc", Universe: 2225,
		Items: []IndustryStrengthItem{
			{Rank: 1, Industry: "半導體業", Stocks: 160, MedianRSScore: 72.5, StrongRatio: 0.33333, WeakRatio: 0.05,
				AvgWeightedReturn: 35.2, PriorMedianRSScore: ptr(60.0), MedianChange: ptr(12.5),
				Leaders: []IndustryLeader{{StockSymbol: "3441", Name: "聯一光", RSScore: 99}, {StockSymbol: "2330", Name: "台積電", RSScore: 80}}},
			{Rank: 2, Industry: "航運業", Stocks: 30, MedianRSScore: 40, StrongRatio: 0, WeakRatio: 0.2,
				AvgWeightedReturn: -3.5, MedianChange: nil, Leaders: []IndustryLeader{}},
		},
	}
}

// TestAPIClientIndustryStrength 驗證 query string、欄位解析與 404／500 的分層。
func TestAPIClientIndustryStrength(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"data_as_of":"2026-10-13","prior_as_of":null,"market":"all","order":"desc","universe":2225,` +
				`"items":[{"rank":1,"industry":"半導體業","stocks":160,"median_rs_score":72.5,"strong_ratio":0.3,"weak_ratio":0.05,` +
				`"avg_weighted_return":35.2,"prior_median_rs_score":null,"median_change":null,` +
				`"leaders":[{"stock_symbol":"3441","name":"聯一光","rs_score":99}]}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.IndustryStrength(t.Context(), IndustryStrengthOptions{Market: "all", MinStocks: 3, Order: "desc"})
	if err != nil || envelope.Universe != 2225 || envelope.PriorAsOf != nil || envelope.Items[0].Leaders[0].RSScore != 99 || envelope.Items[0].MedianChange != nil {
		t.Fatalf("IndustryStrength() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/market/industry-strength" || gotQuery.Encode() != "market=all&min_stocks=3&order=desc" {
		t.Fatalf("未指定日期不應帶 date:%s?%s", gotPath, gotQuery.Encode())
	}
	if _, err := client.IndustryStrength(t.Context(), IndustryStrengthOptions{Date: "2026-10-01", Market: "twse", MinStocks: 5, Order: "asc"}); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if gotQuery.Get("date") != "2026-10-01" || gotQuery.Get("min_stocks") != "5" || gotQuery.Get("order") != "asc" {
		t.Fatalf("query string 錯誤:%s", gotQuery.Encode())
	}
	if _, err := client.IndustryStrength(t.Context(), IndustryStrengthOptions{Market: "tpex", MinStocks: 3, Order: "desc"}); err == nil || errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestIndustryStrengthTool 驗證預設值、參數整理與檢查、錯誤分層、摘要與 structured output。
func TestIndustryStrengthTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeIndustryStrengthQuerier) *industryStrengthToolset {
		return &industryStrengthToolset{is: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeIndustryStrengthQuerier{envelope: sampleIndustryStrength()}
	result, output, err := newToolset(f).industryStrength(t.Context(), nil, IndustryStrengthInput{})
	if err != nil {
		t.Fatalf("industryStrength 不應失敗:%v", err)
	}
	if f.got != (IndustryStrengthOptions{Market: "all", MinStocks: 3, Order: "desc"}) {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(IndustryStrengthOutput); !ok || out.DataKind != "industry_strength" || out.IsRealtime || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"由強到弱", "上市櫃全體 2225 檔", "與 2026-09-14 比較", "第 1 名 半導體業(160 檔):RS 中位數 72.5、變化 +12.5",
		"強勢(≥80)33.3%", "弱勢(≤20)5%", "平均加權報酬 35.2%", "領先:3441 聯一光 99、2330 台積電 80", "第 2 名 航運業(30 檔):RS 中位數 40、變化 無"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}

	in := IndustryStrengthInput{Date: "2026-10-01", Market: " TPEX ", MinStocks: 10, Order: " ASC "}
	if _, _, err := newToolset(f).industryStrength(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	if f.got != (IndustryStrengthOptions{Date: "2026-10-01", Market: "tpex", MinStocks: 10, Order: "asc"}) {
		t.Fatalf("參數應整理後傳給 API:%#v", f.got)
	}
	for _, bad := range []IndustryStrengthInput{{Date: "2026-13-01"}, {Market: "otc"}, {MinStocks: 51}, {Order: "up"}} {
		if _, _, err := newToolset(f).industryStrength(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeIndustryStrengthQuerier{err: ErrMarketDataNotFound}).industryStrength(t.Context(), nil, IndustryStrengthInput{}); err == nil || err.Error() != "指定日期之前查無相對強度資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeIndustryStrengthQuerier{err: errors.New("boom")}).industryStrength(t.Context(), nil, IndustryStrengthInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestIndustryStrengthSummary 驗證 asc 方向、沒有產業的說明與負數變化。
func TestIndustryStrengthSummary(t *testing.T) {
	asc := industryStrengthSummary(&IndustryStrengthEnvelope{DataAsOf: "2026-10-13", Market: "twse", Order: "asc", Universe: 10})
	if !strings.Contains(asc, "由弱到強") || !strings.Contains(asc, "沒有符合檔數門檻的產業") || !strings.Contains(asc, "與 無 比較") {
		t.Errorf("asc 摘要錯誤:%s", asc)
	}
	if signedFloat(ptr(-3.5)) != "-3.5" || signedFloat(ptr(0.0)) != "0" {
		t.Error("signedFloat 負數與 0 不加正號")
	}
}

// TestIndustryStrengthToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestIndustryStrengthToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeIndustryStrengthQuerier{}, nil)
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
		if tool.Name == "get_industry_strength" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_industry_strength 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_industry_strength")
	}
	if len(industryStrengthSchema().Properties["order"].Enum) != 2 {
		t.Error("order 列舉應為 desc、asc")
	}
}
