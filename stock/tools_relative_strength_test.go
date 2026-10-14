// Package stock 測試 get_relative_strength_ranking 的 API client、輸入驗證、摘要與註冊。
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

// fakeRelativeStrengthQuerier 是同時具備基本 Querier 與 RelativeStrengthQuerier 的假資料來源。
type fakeRelativeStrengthQuerier struct {
	fakeQuerier
	envelope *RelativeStrengthEnvelope
	err      error
	got      RelativeStrengthOptions
}

func (f *fakeRelativeStrengthQuerier) RelativeStrength(_ context.Context, opt RelativeStrengthOptions) (*RelativeStrengthEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleRelativeStrength 產生兩檔、符合條件三檔(第三檔被 limit 截掉)的排行。
func sampleRelativeStrength() *RelativeStrengthEnvelope {
	return &RelativeStrengthEnvelope{
		DataAsOf: "2026-10-13", Market: "all", Order: "desc", Universe: 1800, Matched: 3,
		Items: []RelativeStrengthItem{
			{Rank: 1, StockSymbol: "2330", Name: "台積電", Market: "twse", Industry: "半導體業", RSScore: 99,
				WeightedReturn: 45.5, Return20d: ptr(3.2), Return60d: 20, Return120d: 50, Return240d: 80, ClosingPrice: ptr(1450.0)},
			{Rank: 2, StockSymbol: "6488", Name: "環球晶", Market: "tpex", Industry: "半導體業", RSScore: 98,
				WeightedReturn: 40, Return60d: 10, Return120d: 40, Return240d: 80},
		},
	}
}

// TestAPIClientRelativeStrength 驗證 query string、欄位解析與 404／422／500 的分層。
func TestAPIClientRelativeStrength(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		switch gotQuery.Get("industry") {
		case "":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"data_as_of":"2026-10-13","market":"all","order":"desc","universe":1800,"matched":1,` +
				`"items":[{"rank":1,"stock_symbol":"2330","name":"台積電","market":"twse","industry":"半導體業","rs_score":99,` +
				`"weighted_return":45.5,"return_20d":null,"return_60d":20,"return_120d":50,"return_240d":80,"closing_price":1450}]}`))
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

	envelope, err := client.RelativeStrength(t.Context(), RelativeStrengthOptions{Market: "all", Order: "desc", Limit: 30})
	if err != nil || envelope.Universe != 1800 || len(envelope.Items) != 1 {
		t.Fatalf("RelativeStrength() = %#v, %v", envelope, err)
	}
	item := envelope.Items[0]
	if item.RSScore != 99 || item.Return20d != nil || item.Return240d != 80 || *item.ClosingPrice != 1450 {
		t.Fatalf("欄位解析錯誤:%#v", item)
	}
	if gotPath != "/api/v1/market/relative-strength" || gotQuery.Encode() != "limit=30&market=all&order=desc" {
		t.Fatalf("空白條件不應帶入 query:%s?%s", gotPath, gotQuery.Encode())
	}

	if _, err := client.RelativeStrength(t.Context(), RelativeStrengthOptions{
		Date: "2026-10-01", Market: "tpex", Industry: "不存在", Symbol: "6488", Order: "asc", Limit: 5,
	}); !errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("422 應回 ErrIndustryNotFound:%v", err)
	}
	if gotQuery.Get("date") != "2026-10-01" || gotQuery.Get("symbol") != "6488" || gotQuery.Get("order") != "asc" {
		t.Fatalf("query string 錯誤:%s", gotQuery.Encode())
	}
	if _, err := client.RelativeStrength(t.Context(), RelativeStrengthOptions{Industry: "沒資料"}); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if _, err := client.RelativeStrength(t.Context(), RelativeStrengthOptions{Industry: "故障"}); err == nil || errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestRelativeStrengthTool 驗證預設值、參數整理與檢查、錯誤分層與 structured output。
func TestRelativeStrengthTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeRelativeStrengthQuerier) *relativeStrengthToolset {
		return &relativeStrengthToolset{rs: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeRelativeStrengthQuerier{envelope: sampleRelativeStrength()}
	result, output, err := newToolset(f).relativeStrength(t.Context(), nil, RelativeStrengthInput{})
	if err != nil {
		t.Fatalf("relativeStrength 不應失敗:%v", err)
	}
	if f.got != (RelativeStrengthOptions{Market: "all", Order: "desc", Limit: 30}) {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(RelativeStrengthOutput); !ok || out.DataKind != "relative_strength" || out.IsRealtime || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"全體 1800 檔", "由強到弱", "第 1 名 2330 台積電", "RS 99", "近 20 日 3.2%", "近 20 日 無", "另有 1 檔未列出"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}

	in := RelativeStrengthInput{Date: "2026-10-01", Market: " TPEX ", Industry: " 半導體業 ", Symbol: " 6488 ", Order: " ASC ", Limit: 5}
	if _, _, err := newToolset(f).relativeStrength(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	want := RelativeStrengthOptions{Date: "2026-10-01", Market: "tpex", Industry: "半導體業", Symbol: "6488", Order: "asc", Limit: 5}
	if f.got != want {
		t.Fatalf("參數應整理後傳給 API:%#v", f.got)
	}
	for _, bad := range []RelativeStrengthInput{
		{Date: "2026-13-01"}, {Market: "otc"}, {Industry: strings.Repeat("業", 21)},
		{Symbol: strings.Repeat("9", 25)}, {Order: "up"}, {Limit: 201},
	} {
		if _, _, err := newToolset(f).relativeStrength(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	notFound := &fakeRelativeStrengthQuerier{err: ErrMarketDataNotFound}
	if _, _, err := newToolset(notFound).relativeStrength(t.Context(), nil, RelativeStrengthInput{}); err == nil || err.Error() != "指定日期之前查無相對強度資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(notFound).relativeStrength(t.Context(), nil, RelativeStrengthInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號 9999") {
		t.Fatalf("帶代號的 404 應提示代號:%v", err)
	}
	if _, _, err := newToolset(&fakeRelativeStrengthQuerier{err: ErrIndustryNotFound}).relativeStrength(t.Context(), nil, RelativeStrengthInput{Industry: "半導體"}); err == nil || !strings.Contains(err.Error(), "找不到產業「半導體」") {
		t.Fatalf("422 應提示產業名稱:%v", err)
	}
	if _, _, err := newToolset(&fakeRelativeStrengthQuerier{err: errors.New("boom")}).relativeStrength(t.Context(), nil, RelativeStrengthInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestRelativeStrengthSummary 驗證 asc 方向、單一股票未計分與沒有符合條件的說明。
func TestRelativeStrengthSummary(t *testing.T) {
	asc := relativeStrengthSummary(&RelativeStrengthEnvelope{DataAsOf: "2026-10-13", Market: "twse", Order: "asc", Universe: 10}, "")
	if !strings.Contains(asc, "由弱到強") || !strings.Contains(asc, "沒有符合條件的股票") {
		t.Errorf("asc 摘要錯誤:%s", asc)
	}
	one := relativeStrengthSummary(&RelativeStrengthEnvelope{DataAsOf: "2026-10-13", Market: "all", Order: "desc", Universe: 10}, "7799")
	if !strings.Contains(one, "7799 未計分") {
		t.Errorf("單一股票摘要錯誤:%s", one)
	}
}

// TestRelativeStrengthToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestRelativeStrengthToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeRelativeStrengthQuerier{}, nil)
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
		if tool.Name == "get_relative_strength_ranking" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_relative_strength_ranking 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_relative_strength_ranking")
	}
	if len(relativeStrengthSchema().Properties["order"].Enum) != 2 {
		t.Error("order 列舉應為 desc、asc")
	}
}
