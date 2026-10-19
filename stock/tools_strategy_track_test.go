// Package stock 測試 get_strategy_track 的 API client、輸入驗證、摘要與註冊。
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

// fakeTrackQuerier 是同時具備基本 Querier 與 StrategyTrackQuerier 的假資料來源。
type fakeTrackQuerier struct {
	fakeQuerier
	envelope *StrategyTrackEnvelope
	err      error
	got      string
}

func (f *fakeTrackQuerier) StrategyTrack(_ context.Context, preset string) (*StrategyTrackEnvelope, error) {
	f.got = preset
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

func trackHolding(symbol string, weight float64, ret *float64) StrategyTrackHolding {
	return StrategyTrackHolding{Symbol: symbol, Weight: weight, ReturnPercent: ret}
}

func sampleTrack() *StrategyTrackEnvelope {
	return &StrategyTrackEnvelope{
		Preset: "最佳組合", From: ptr("2026-10-01"), To: ptr("2026-10-20"),
		CumulativeReturn: ptr(4.1), EtfCumulativeReturn: ptr(2.5), ExcessReturn: ptr(1.6),
		Periods: []StrategyTrackPeriod{{
			Date: "2026-10-01", End: "2026-10-20", Ongoing: true, HoldDays: 20, EtfWeight: 50, Cash: 10,
			Holdings: []StrategyTrackHolding{
				{Symbol: "2330", Name: ptr("台積電"), Weight: 10, ReturnPercent: ptr(5.0)},
				trackHolding("A", 10, ptr(-3.0)), trackHolding("B", 10, ptr(8.0)), trackHolding("C", 10, ptr(1.0)),
				trackHolding("D", 10, ptr(-7.0)), trackHolding("E", 10, ptr(2.0)), trackHolding("F", 10, nil),
			},
			StrategyReturn: ptr(5.7), EtfReturn: ptr(2.5), PortfolioReturn: ptr(4.1),
		}},
	}
}

// TestAPIClientStrategyTrack 驗證路徑跳脫與 404 語意。
func TestAPIClientStrategyTrack(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		if r.URL.Path != "/api/v1/strategy-presets/最佳組合/track" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"preset":"最佳組合","from":"2026-10-01","to":"2026-10-20","cumulative_return":4.1,"etf_cumulative_return":2.5,` +
			`"excess_return":1.6,"periods":[{"date":"2026-10-01","end":"2026-10-20","ongoing":true,"hold_days":20,"etf_weight":50,"cash":10,` +
			`"market_filtered":false,"holdings":[{"symbol":"2330","name":"台積電","weight":90,"return_percent":5}],"strategy_return":4.5,` +
			`"etf_return":2.5,"portfolio_return":3.5}]}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	track, err := client.StrategyTrack(t.Context(), "最佳組合")
	if err != nil || len(track.Periods) != 1 || *track.Periods[0].Holdings[0].ReturnPercent != 5 || *track.CumulativeReturn != 4.1 {
		t.Fatalf("StrategyTrack() = %#v, %v", track, err)
	}
	if !strings.Contains(gotPath, "%E6%9C%80") {
		t.Fatalf("策略名稱應跳脫:%s", gotPath)
	}
	if _, err := client.StrategyTrack(t.Context(), "不存在"); !errors.Is(err, ErrPresetNotFound) {
		t.Fatalf("404 應回 ErrPresetNotFound:%v", err)
	}
}

// TestStrategyTrackTool 驗證名稱檢查、錯誤分層與摘要(預設只列最好與最差各 3 檔)。
func TestStrategyTrackTool(t *testing.T) {
	newToolset := func(f *fakeTrackQuerier) *strategyTrackToolset {
		return &strategyTrackToolset{tq: f, logf: func(string, ...any) {}}
	}
	f := &fakeTrackQuerier{envelope: sampleTrack()}
	result, output, err := newToolset(f).strategyTrack(t.Context(), nil, StrategyTrackInput{Preset: " 最佳組合 "})
	if err != nil || f.got != "最佳組合" {
		t.Fatalf("strategyTrack 失敗或名稱未去空白:%v %q", err, f.got)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"具名策略「最佳組合」實盤追蹤(依換股通知當天記錄的持股,還原報酬、未扣交易成本):2026-10-01～2026-10-20 共 1 期,累積 4.1%,同期持有 0050 2.5%,超額 1.6 個百分點。",
		"- 2026-10-01 → 2026-10-20(持有中):組合 4.1%(策略部位 5.7%、0050 2.5%,0050 核心 50%,策略部位現金 10%);持股 7 檔:",
		"最好 B 10% 8%、2330 台積電 10% 5%、E 10% 2%;最差 D 10% -7%、A 10% -3%、C 10% 1%",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(StrategyTrackOutput); !ok || out.DataKind != "strategy_track" {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	all, _, err := newToolset(f).strategyTrack(t.Context(), nil, StrategyTrackInput{Preset: "最佳組合", Holdings: true})
	if err != nil || !strings.Contains(all.Content[0].(*mcp.TextContent).Text, "F 10% 無") {
		t.Fatalf("holdings=true 應列出全部持股:%v", err)
	}

	if _, _, err := newToolset(f).strategyTrack(t.Context(), nil, StrategyTrackInput{Preset: " "}); err == nil {
		t.Fatal("空白名稱應回錯誤")
	}
	if _, _, err := newToolset(&fakeTrackQuerier{err: ErrPresetNotFound}).strategyTrack(t.Context(), nil, StrategyTrackInput{Preset: "不存在"}); err == nil || !strings.Contains(err.Error(), "找不到具名策略「不存在」") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeTrackQuerier{err: errors.New("boom")}).strategyTrack(t.Context(), nil, StrategyTrackInput{Preset: "最佳組合"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestStrategyTrackSummaryEdgeCases 涵蓋還沒有紀錄、空手與持股都查不到報酬。
func TestStrategyTrackSummaryEdgeCases(t *testing.T) {
	empty := strategyTrackSummary(&StrategyTrackEnvelope{Preset: "甲"}, false)
	if !strings.Contains(empty, "具名策略「甲」還沒有換股紀錄") {
		t.Fatalf("摘要錯誤:%s", empty)
	}
	idle := strategyTrackSummary(&StrategyTrackEnvelope{Preset: "甲", From: ptr("a"), To: ptr("b"), Periods: []StrategyTrackPeriod{
		{Date: "a", End: "b", MarketFiltered: true, Cash: 100, StrategyReturn: ptr(0.0), PortfolioReturn: ptr(0.0)},
	}}, false)
	if !strings.Contains(idle, "大盤濾網沒過,策略部位空手") || !strings.Contains(idle, "累積 無") {
		t.Fatalf("空手摘要錯誤:%s", idle)
	}
	unknown := make([]StrategyTrackHolding, 7)
	for i := range unknown {
		unknown[i] = trackHolding(string(rune('A'+i)), 10, nil)
	}
	if lines := trackHoldingTexts(unknown, false); lines != nil {
		t.Fatalf("持股都查不到報酬時不列:%v", lines)
	}
}

// TestStrategyTrackToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestStrategyTrackToolIsRegisteredReadOnly(t *testing.T) {
	assertToolRegisteredReadOnly(t, &fakeTrackQuerier{}, "get_strategy_track")
}
