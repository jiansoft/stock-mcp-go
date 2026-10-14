// Package stock 測試 get_margin_ranking 的 API client、輸入驗證、摘要與註冊。
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

// fakeMarginRankingQuerier 是同時具備基本 Querier 與 MarginRankingQuerier 的假資料來源。
type fakeMarginRankingQuerier struct {
	fakeQuerier
	envelope *MarginRankingEnvelope
	err      error
	got      MarginRankingOptions
}

func (f *fakeMarginRankingQuerier) MarginRanking(_ context.Context, opt MarginRankingOptions) (*MarginRankingEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleMarginRanking 產生兩檔融資增加,其中一檔未分類、沒有百分比與收盤價。
func sampleMarginRanking() *MarginRankingEnvelope {
	return &MarginRankingEnvelope{
		DataAsOf: "2026-10-14", From: "2026-09-16", Days: 20, RankBy: "margin_increase", Market: "all", Matched: 800,
		Items: []MarginRankItem{
			{Rank: 1, StockSymbol: "2409", Name: "友達", Market: "twse", Industry: "光電業", MarginBalance: 307335, MarginChange: 53666,
				MarginChangePercent: ptr(21.15), ShortBalance: 12525, ShortChange: 6944, ShortMarginRatio: ptr(4.08), ClosingPrice: ptr(15.2)},
			{Rank: 2, StockSymbol: "6456", Name: "GIS-KY", Market: "twse", MarginBalance: 33277, MarginChange: 16816,
				ShortBalance: 485, ShortChange: -292},
		},
	}
}

// TestAPIClientMarginRanking 驗證 query string、欄位解析與 404／422／500 的分層。
func TestAPIClientMarginRanking(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"data_as_of":"2026-10-14","from":"2026-10-14","days":1,"rank_by":"margin_increase",` +
				`"market":"all","industry":null,"matched":1,"items":[{"rank":1,"stock_symbol":"2303","name":"聯電","market":"twse",` +
				`"industry":"半導體業","margin_balance":169468,"margin_change":5129,"margin_change_percent":null,` +
				`"short_balance":3580,"short_change":-32,"short_margin_ratio":2.11,"closing_price":null}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	opt := MarginRankingOptions{Days: 1, RankBy: "margin_increase", Market: "all", Limit: 30}
	envelope, err := client.MarginRanking(t.Context(), opt)
	if err != nil || envelope.Industry != nil || envelope.Items[0].MarginChange != 5129 || envelope.Items[0].MarginChangePercent != nil || *envelope.Items[0].ShortMarginRatio != 2.11 {
		t.Fatalf("MarginRanking() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/market/margin-ranking" || gotQuery.Encode() != "days=1&limit=30&market=all&rank_by=margin_increase" {
		t.Fatalf("未指定日期與產業不應帶參數:%s?%s", gotPath, gotQuery.Encode())
	}
	opt = MarginRankingOptions{Date: "2026-10-01", Days: 5, RankBy: "short_margin_ratio", Market: "twse", Industry: "半導體業", Limit: 10}
	if _, err := client.MarginRanking(t.Context(), opt); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if gotQuery.Get("date") != "2026-10-01" || gotQuery.Get("industry") != "半導體業" || gotQuery.Get("rank_by") != "short_margin_ratio" {
		t.Fatalf("query string 錯誤:%s", gotQuery.Encode())
	}
	opt.Market = "tpex"
	if _, err := client.MarginRanking(t.Context(), opt); !errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("422 應回 ErrIndustryNotFound:%v", err)
	}
	opt.Market = "none"
	if _, err := client.MarginRanking(t.Context(), opt); err == nil || errors.Is(err, ErrMarketDataNotFound) || errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestMarginRankingTool 驗證預設值、參數整理與檢查、錯誤分層、摘要與 structured output。
func TestMarginRankingTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeMarginRankingQuerier) *marginRankingToolset {
		return &marginRankingToolset{mq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeMarginRankingQuerier{envelope: sampleMarginRanking()}
	result, output, err := newToolset(f).marginRanking(t.Context(), nil, MarginRankingInput{})
	if err != nil {
		t.Fatalf("marginRanking 不應失敗:%v", err)
	}
	if f.got != (MarginRankingOptions{Days: 1, RankBy: "margin_increase", Market: "all", Limit: 30}) {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(MarginRankingOutput); !ok || out.DataKind != "margin_ranking" || out.IsRealtime || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"融資增加排行(2026-09-16 至 2026-10-14 共 20 個交易日,all 市場)", "符合 800 檔",
		"第 1 名 2409 友達(twse,光電業):融資 307335 張(+53666 張,21.15%)、融券 12525 張(+6944 張)、券資比 4.08%;收盤 15.2",
		"第 2 名 6456 GIS-KY(twse,未分類):融資 33277 張(+16816 張,無)、融券 485 張(-292 張)、券資比 無;收盤 無"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}

	in := MarginRankingInput{Date: "2026-10-01", Days: 20, RankBy: " Short_Margin_Ratio ", Market: " TPEX ", Industry: " 半導體業 ", Limit: 100}
	if _, _, err := newToolset(f).marginRanking(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	want := MarginRankingOptions{Date: "2026-10-01", Days: 20, RankBy: "short_margin_ratio", Market: "tpex", Industry: "半導體業", Limit: 100}
	if f.got != want {
		t.Fatalf("參數應整理後傳給 API:%#v", f.got)
	}
	for _, bad := range []MarginRankingInput{
		{Date: "2026-13-01"}, {Days: 61}, {RankBy: "volume"}, {Market: "otc"}, {Industry: strings.Repeat("產", 21)}, {Limit: 101},
	} {
		if _, _, err := newToolset(f).marginRanking(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeMarginRankingQuerier{err: ErrMarketDataNotFound}).marginRanking(t.Context(), nil, MarginRankingInput{}); err == nil || err.Error() != "指定日期之前查無籌碼資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeMarginRankingQuerier{err: ErrIndustryNotFound}).marginRanking(t.Context(), nil, MarginRankingInput{Industry: "不存在"}); err == nil || !strings.Contains(err.Error(), "找不到產業「不存在」") {
		t.Fatalf("422 應提示產業名稱:%v", err)
	}
	if _, _, err := newToolset(&fakeMarginRankingQuerier{err: errors.New("boom")}).marginRanking(t.Context(), nil, MarginRankingInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestMarginRankingSummary 驗證其他排序鍵、產業條件與沒有股票的說明。
func TestMarginRankingSummary(t *testing.T) {
	empty := marginRankingSummary(&MarginRankingEnvelope{DataAsOf: "2026-10-14", From: "2026-10-14", Days: 1,
		RankBy: "short_decrease", Market: "twse", Industry: ptr("航運業")})
	for _, want := range []string{"融券減少排行", "twse 市場、航運業", "沒有符合條件的股票"} {
		if !strings.Contains(empty, want) {
			t.Errorf("摘要缺少 %q:%s", want, empty)
		}
	}
}

// TestMarginRankingToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestMarginRankingToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeMarginRankingQuerier{}, nil)
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
		if tool.Name == "get_margin_ranking" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_margin_ranking 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_margin_ranking")
	}
	if len(marginRankingSchema().Properties["rank_by"].Enum) != 5 {
		t.Error("rank_by 應有 5 個列舉")
	}
}
