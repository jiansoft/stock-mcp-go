// Package stock 測試 get_relative_strength_history 的 API client、輸入驗證、摘要與註冊。
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

// fakeRelativeStrengthHistoryQuerier 是同時具備基本 Querier 與 RelativeStrengthHistoryQuerier 的假資料來源。
type fakeRelativeStrengthHistoryQuerier struct {
	fakeQuerier
	envelope  *RelativeStrengthHistoryEnvelope
	err       error
	gotSymbol string
	gotDate   string
	gotDays   int
}

func (f *fakeRelativeStrengthHistoryQuerier) RelativeStrengthHistory(_ context.Context, symbol, date string, days int) (*RelativeStrengthHistoryEnvelope, error) {
	f.gotSymbol, f.gotDate, f.gotDays = symbol, date, days
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleRelativeStrengthHistory 產生三個交易日、一次升破 80 的歷史。
func sampleRelativeStrengthHistory() *RelativeStrengthHistoryEnvelope {
	return &RelativeStrengthHistoryEnvelope{
		StockSymbol: "2330", DataAsOf: ptr("2026-10-13"), Days: 60,
		LatestScore: ptr(85), HighScore: ptr(85), HighDate: ptr("2026-10-13"),
		LowScore: ptr(70), LowDate: ptr("2026-10-08"), ScoreChange: ptr(15),
		Crossings: []RelativeStrengthCrossing{
			{Date: "2026-10-13", Threshold: 80, Direction: "up", FromScore: 78, ToScore: 85},
			{Date: "2026-10-08", Threshold: 80, Direction: "down", FromScore: 81, ToScore: 70},
		},
		Items: []RelativeStrengthPoint{
			{Date: "2026-10-13", RSScore: 85, Rank: 330, Universe: 2225, WeightedReturn: 30.5},
			{Date: "2026-10-12", RSScore: 78, Rank: 480, Universe: 2228, WeightedReturn: 25},
			{Date: "2026-10-08", RSScore: 70, Rank: 650, Universe: 2225, WeightedReturn: 20},
		},
	}
}

// TestAPIClientRelativeStrengthHistory 驗證路徑、query string、欄位解析與 404／500 的分層。
func TestAPIClientRelativeStrengthHistory(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		switch {
		case strings.Contains(r.URL.Path, "/9999/"):
			w.WriteHeader(http.StatusNotFound)
		case strings.Contains(r.URL.Path, "/5555/"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":"2026-10-13","days":2,"latest_score":85,"high_score":85,` +
				`"high_date":"2026-10-13","low_score":78,"low_date":"2026-10-12","score_change":7,` +
				`"crossings":[{"date":"2026-10-13","threshold":80,"direction":"up","from_score":78,"to_score":85}],` +
				`"items":[{"date":"2026-10-13","rs_score":85,"rank":330,"universe":2225,"weighted_return":30.5}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.RelativeStrengthHistory(t.Context(), "2330", "2026-10-13", 2)
	if err != nil || *envelope.LatestScore != 85 || *envelope.ScoreChange != 7 || len(envelope.Crossings) != 1 || envelope.Items[0].Universe != 2225 {
		t.Fatalf("RelativeStrengthHistory() = %#v, %v", envelope, err)
	}
	if gotPath != "/api/v1/stocks/2330/relative-strength" || gotQuery.Encode() != "date=2026-10-13&days=2" {
		t.Fatalf("路徑或 query 錯誤:%s?%s", gotPath, gotQuery.Encode())
	}
	if _, err := client.RelativeStrengthHistory(t.Context(), "2330", "", 60); err != nil || gotQuery.Encode() != "days=60" {
		t.Fatalf("未指定日期不應帶 date:%s, %v", gotQuery.Encode(), err)
	}
	if _, err := client.RelativeStrengthHistory(t.Context(), "9999", "", 60); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
	if _, err := client.RelativeStrengthHistory(t.Context(), "5555", "", 60); err == nil || errors.Is(err, ErrStockNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestRelativeStrengthHistoryTool 驗證預設值、參數檢查、錯誤分層、摘要與 structured output。
func TestRelativeStrengthHistoryTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeRelativeStrengthHistoryQuerier) *relativeStrengthHistoryToolset {
		return &relativeStrengthHistoryToolset{rs: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeRelativeStrengthHistoryQuerier{envelope: sampleRelativeStrengthHistory()}
	result, output, err := newToolset(f).relativeStrengthHistory(t.Context(), nil, RelativeStrengthHistoryInput{Symbol: " 2330 "})
	if err != nil {
		t.Fatalf("relativeStrengthHistory 不應失敗:%v", err)
	}
	if f.gotSymbol != "2330" || f.gotDate != "" || f.gotDays != 60 {
		t.Fatalf("預設條件錯誤:%q %q %d", f.gotSymbol, f.gotDate, f.gotDays)
	}
	if out, ok := output.(RelativeStrengthHistoryOutput); !ok || out.DataKind != "relative_strength_history" || out.IsRealtime || len(out.Items) != 3 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"2026-10-13 為 RS 85(第 330 名/2225 檔)", "期間最高 85(2026-10-13)", "最低 70(2026-10-08)",
		"期間變化 +15 分", "2026-10-13 升破 80 分(78 → 85)", "2026-10-08 跌破 80 分(81 → 70)", "2026-10-12 RS 78(第 480 名)、加權 25%"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}

	if _, _, err := newToolset(f).relativeStrengthHistory(t.Context(), nil, RelativeStrengthHistoryInput{Symbol: "2330", Date: "2026-10-01", Days: 120}); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	if f.gotDate != "2026-10-01" || f.gotDays != 120 {
		t.Fatalf("參數應傳給 API:%q %d", f.gotDate, f.gotDays)
	}
	for _, bad := range []RelativeStrengthHistoryInput{{}, {Symbol: "2330", Date: "2026-13-01"}, {Symbol: "2330", Days: 121}, {Symbol: "2330", Days: -1}} {
		if _, _, err := newToolset(f).relativeStrengthHistory(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeRelativeStrengthHistoryQuerier{err: ErrStockNotFound}).relativeStrengthHistory(t.Context(), nil, RelativeStrengthHistoryInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號:9999") {
		t.Fatalf("404 應提示代號:%v", err)
	}
	if _, _, err := newToolset(&fakeRelativeStrengthHistoryQuerier{err: errors.New("boom")}).relativeStrengthHistory(t.Context(), nil, RelativeStrengthHistoryInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestRelativeStrengthHistorySummary 驗證未計分、沒有轉折與逐日列數上限的說明。
func TestRelativeStrengthHistorySummary(t *testing.T) {
	empty := relativeStrengthHistorySummary(&RelativeStrengthHistoryEnvelope{StockSymbol: "7799", Days: 60})
	if !strings.Contains(empty, "7799 近 60 個交易日沒有相對強度分數") {
		t.Errorf("未計分摘要錯誤:%s", empty)
	}
	items := make([]RelativeStrengthPoint, 25)
	for i := range items {
		items[i] = RelativeStrengthPoint{Date: "2026-10-13", RSScore: 50, Rank: 1000, Universe: 2000}
	}
	long := relativeStrengthHistorySummary(&RelativeStrengthHistoryEnvelope{
		StockSymbol: "2330", DataAsOf: ptr("2026-10-13"), Days: 25, Items: items,
	})
	if !strings.Contains(long, "期間內沒有穿越 50 或 80 分") || !strings.Contains(long, "另有 5 個交易日未列出") || !strings.Contains(long, "期間變化 無 分") {
		t.Errorf("長期間摘要錯誤:%s", long)
	}
}

// TestRelativeStrengthHistoryToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestRelativeStrengthHistoryToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeRelativeStrengthHistoryQuerier{}, nil)
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
		if tool.Name == "get_relative_strength_history" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_relative_strength_history 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_relative_strength_history")
	}
	if relativeStrengthHistorySchema().Required[0] != "symbol" {
		t.Error("symbol 必須為必填")
	}
}
