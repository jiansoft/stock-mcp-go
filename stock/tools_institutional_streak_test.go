// Package stock 測試 get_institutional_streak_performance 的 API client、輸入驗證、摘要與註冊。
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

// fakeInstitutionalStreakQuerier 是同時具備基本 Querier 與 InstitutionalStreakQuerier 的假資料來源。
type fakeInstitutionalStreakQuerier struct {
	fakeQuerier
	envelope *InstitutionalStreakEnvelope
	err      error
	got      InstitutionalStreakOptions
}

func (f *fakeInstitutionalStreakQuerier) InstitutionalStreakPerformance(_ context.Context, opt InstitutionalStreakOptions) (*InstitutionalStreakEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleInstitutionalStreak 產生 5 日有樣本、60 日尚無樣本的投信連買 3 天統計。
func sampleInstitutionalStreak() *InstitutionalStreakEnvelope {
	return &InstitutionalStreakEnvelope{
		From: "2025-10-15", To: "2026-10-14", Market: "all", Investor: "trust", Side: "buy", Streak: 3,
		Adjusted: true, Occurrences: 812,
		Horizons: []HorizonStat{
			{Days: 5, Samples: 800, AvgReturn: ptr(1.12), MedianReturn: ptr(0.5), WinRate: ptr(0.5432),
				AvgExcessReturn: ptr(0.61), BeatMarketRate: ptr(0.5211)},
			{Days: 60, Samples: 0},
		},
		Benchmark: []BenchmarkStat{
			{Days: 5, Samples: 450000, AvgReturn: ptr(0.51), WinRate: ptr(0.4987)},
			{Days: 60, Samples: 0},
		},
	}
}

// TestAPIClientInstitutionalStreakPerformance 驗證路徑、query string、欄位解析與 404／422／500 的分層。
func TestAPIClientInstitutionalStreakPerformance(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"from":"2025-10-15","to":"2026-10-14","market":"all","investor":"foreign","side":"buy",` +
				`"streak":3,"industry":null,"adjusted":true,"occurrences":2,` +
				`"horizons":[{"days":5,"samples":2,"avg_return":1.0,"median_return":1.0,"win_rate":0.5,` +
				`"avg_excess_return":2.0,"beat_market_rate":0.5}],` +
				`"benchmark":[{"days":5,"samples":6,"avg_return":0.5,"win_rate":0.5}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	opt := InstitutionalStreakOptions{Investor: "foreign", Side: "buy", Streak: 3, Market: "all"}
	envelope, err := client.InstitutionalStreakPerformance(t.Context(), opt)
	if err != nil || envelope.Industry != nil || envelope.Occurrences != 2 || *envelope.Horizons[0].AvgExcessReturn != 2.0 || envelope.Benchmark[0].Samples != 6 {
		t.Fatalf("InstitutionalStreakPerformance() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/market/institutional-streak/performance" || gotQuery.Encode() != "investor=foreign&market=all&side=buy&streak=3" {
		t.Fatalf("未指定起訖與產業不應帶參數:%s?%s", gotPath, gotQuery.Encode())
	}
	opt = InstitutionalStreakOptions{From: "2026-01-01", To: "2026-06-30", Investor: "trust", Side: "sell", Streak: 5, Market: "twse", Industry: "半導體業", GroupBy: "year"}
	if _, err := client.InstitutionalStreakPerformance(t.Context(), opt); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	if gotQuery.Get("from") != "2026-01-01" || gotQuery.Get("to") != "2026-06-30" || gotQuery.Get("industry") != "半導體業" || gotQuery.Get("streak") != "5" || gotQuery.Get("group_by") != "year" {
		t.Fatalf("query string 錯誤:%s", gotQuery.Encode())
	}
	opt.Market = "tpex"
	if _, err := client.InstitutionalStreakPerformance(t.Context(), opt); !errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("422 應回 ErrIndustryNotFound:%v", err)
	}
	opt.Market = "none"
	if _, err := client.InstitutionalStreakPerformance(t.Context(), opt); err == nil || errors.Is(err, ErrMarketDataNotFound) || errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestInstitutionalStreakTool 驗證預設值、參數整理與檢查、錯誤分層、摘要與 structured output。
func TestInstitutionalStreakTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeInstitutionalStreakQuerier) *institutionalStreakToolset {
		return &institutionalStreakToolset{sq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeInstitutionalStreakQuerier{envelope: sampleInstitutionalStreak()}
	result, output, err := newToolset(f).institutionalStreakPerformance(t.Context(), nil, InstitutionalStreakInput{})
	if err != nil {
		t.Fatalf("institutionalStreakPerformance 不應失敗:%v", err)
	}
	if f.got != (InstitutionalStreakOptions{Investor: "foreign", Side: "buy", Streak: 3, Market: "all"}) {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(InstitutionalStreakOutput); !ok || out.DataKind != "institutional_streak_performance" || out.IsRealtime || out.Occurrences != 812 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"投信 連續買超滿 3 天之後的表現(2025-10-15～2026-10-14,all 市場;",
		"共 812 次事件",
		"大盤基準:5 日 平均 0.51%、上漲比例 49.87%;60 日 尚無樣本。",
		"事件之後:5 日 平均 1.12%、中位數 0.5%、勝率 54.32%、超額 0.61%、勝過大盤 52.11%(樣本 800);60 日 尚無樣本。",
		"過去表現不代表未來結果",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}

	in := InstitutionalStreakInput{From: "2026-01-01", To: "2026-06-30", Investor: " TOTAL ", Side: " Sell ", Streak: 20, Market: " TPEX ", Industry: " 半導體業 ", GroupBy: "year"}
	if _, _, err := newToolset(f).institutionalStreakPerformance(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	want := InstitutionalStreakOptions{From: "2026-01-01", To: "2026-06-30", Investor: "total", Side: "sell", Streak: 20, Market: "tpex", Industry: "半導體業", GroupBy: "year"}
	if f.got != want {
		t.Fatalf("參數應整理後傳給 API:%#v", f.got)
	}
	for _, bad := range []InstitutionalStreakInput{
		{From: "2026-13-01"}, {To: "bad"}, {From: "2026-06-30", To: "2026-01-01"}, {From: "2016-01-01", To: "2026-01-02"},
		{Investor: "retail"}, {Side: "hold"}, {Streak: 21}, {Streak: -1}, {Market: "otc"}, {Industry: strings.Repeat("產", 21)},
		{GroupBy: "week"},
	} {
		if _, _, err := newToolset(f).institutionalStreakPerformance(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeInstitutionalStreakQuerier{err: ErrMarketDataNotFound}).institutionalStreakPerformance(t.Context(), nil, InstitutionalStreakInput{}); err == nil || err.Error() != "指定迄日之前查無籌碼資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeInstitutionalStreakQuerier{err: ErrIndustryNotFound}).institutionalStreakPerformance(t.Context(), nil, InstitutionalStreakInput{Industry: "不存在"}); err == nil || !strings.Contains(err.Error(), "找不到產業「不存在」") {
		t.Fatalf("422 應提示產業名稱:%v", err)
	}
	if _, _, err := newToolset(&fakeInstitutionalStreakQuerier{err: errors.New("boom")}).institutionalStreakPerformance(t.Context(), nil, InstitutionalStreakInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestInstitutionalStreakSummary 驗證賣超方向、產業條件與沒有事件的說明。
func TestInstitutionalStreakSummary(t *testing.T) {
	sell := institutionalStreakSummary(&InstitutionalStreakEnvelope{From: "2026-01-01", To: "2026-06-30", Market: "twse",
		Investor: "dealer", Side: "sell", Streak: 5, Industry: ptr("航運業"), Adjusted: true})
	for _, want := range []string{"自營商 連續賣超滿 5 天", "twse 市場、產業「航運業」", "共 0 次事件", "期間內沒有符合條件的事件"} {
		if !strings.Contains(sell, want) {
			t.Errorf("賣超摘要缺少 %q:%s", want, sell)
		}
	}
	if strings.Contains(sell, "大盤基準") || strings.Contains(sell, "事件之後") {
		t.Errorf("沒有基準與事件時不應列出:%s", sell)
	}

	yearly := sampleInstitutionalStreak()
	yearly.GroupBy = ptr("year")
	yearly.BenchmarkByYear = []BenchmarkYear{{Year: 2025, Benchmark: []BenchmarkStat{{Days: 5, Samples: 10, AvgReturn: ptr(0.3), WinRate: ptr(0.5)}}}}
	yearly.Years = []PerformanceYear{{Year: 2025, Occurrences: 300, Horizons: []HorizonStat{
		{Days: 5, Samples: 300, AvgReturn: ptr(0.9), MedianReturn: ptr(0.2), WinRate: ptr(0.52), AvgExcessReturn: ptr(0.6), BeatMarketRate: ptr(0.5)},
	}}}
	text := institutionalStreakSummary(yearly)
	for _, want := range []string{
		"各年度大盤基準:2025 年 5 日 平均 0.3%、上漲比例 50%。",
		"\n- 2025 年(出現 300 次):5 日 平均 0.9%、中位數 0.2%、勝率 52%、超額 0.6%、勝過大盤 50%(樣本 300)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("年度摘要缺少 %q:%s", want, text)
		}
	}
}

// TestInstitutionalStreakToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestInstitutionalStreakToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeInstitutionalStreakQuerier{}, nil)
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
		if tool.Name == "get_institutional_streak_performance" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_institutional_streak_performance 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_institutional_streak_performance")
	}
	schema := institutionalStreakSchema()
	if len(schema.Properties["investor"].Enum) != 4 || len(schema.Properties["side"].Enum) != 2 || *schema.Properties["streak"].Maximum != 20 {
		t.Error("investor 應有 4 個列舉、side 應有 2 個列舉、streak 上限 20")
	}
}
