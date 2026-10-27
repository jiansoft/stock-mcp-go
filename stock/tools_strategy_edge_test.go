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

// fakeEdgeQuerier 提供工具註冊與錯誤測試的資料來源。
type fakeEdgeQuerier struct {
	fakeQuerier
	envelope *StrategyEdgeEnvelope
	err      error
}

func (q *fakeEdgeQuerier) StrategyEdge(context.Context) (*StrategyEdgeEnvelope, error) {
	return q.envelope, q.err
}

// TestStrategyEdgeAPIAndTool 透過假 Data API 驗證 HTTP、結構化輸出與兩段摘要。
func TestStrategyEdgeAPIAndTool(t *testing.T) {
	payload := `{"strategies":[{"name":"最佳組合","checked":"2026-10-27","recent":{"from":"2024-10-27","to":"2026-10-27","offsets":20,"median_return":66.56,"etf_median_return":59.78,"beat_rate":0.75},"full":{"from":"2019-01-02","to":"2026-10-27","offsets":20,"median_return":31.77,"etf_median_return":30.34,"beat_rate":0.65},"verdict":"holds"}],"execution_price":"next_open","slippage_bps":20,"disclaimer":"僅供參考，不構成投資建議，亦不會自動下單"}`
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"found", 200, payload, false}, {"missing", 404, "", true}, {"corrupt", 200, "broken", true}, {"unavailable", 503, "private details", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/strategy-presets/edge" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewAPIClient(server.URL, "secret", time.Second)
			ts := &strategyEdgeToolset{q: client, logf: func(string, ...any) {}}
			result, out, err := ts.strategyEdge(t.Context(), nil, struct{}{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr {
				if strings.Contains(err.Error(), "private details") {
					t.Fatal("leaked details")
				}
				if tc.status == 404 && !strings.Contains(err.Error(), "尚無") {
					t.Fatalf("missing: %v", err)
				}
				return
			}
			got := out.(StrategyEdgeOutput)
			if got.IsRealtime || got.DataKind != "strategy_edge" || *got.Strategies[0].Recent.MedianReturn != 66.56 {
				t.Fatalf("output = %#v", got)
			}
			summary := result.Content[0].(*mcp.TextContent).Text
			for _, want := range []string{"2026-10-27", "66.56%", "59.78%", "75%", "31.77%", "65%", "holds", "get_screen_backtest_offsets", "亦不會自動下單"} {
				if !strings.Contains(summary, want) {
					t.Errorf("missing %q: %s", want, summary)
				}
			}
		})
	}
}

// TestStrategyEdgeUnknownAndErrors 保留 null 與 unknown，並掩蔽來源錯誤。
func TestStrategyEdgeUnknownAndErrors(t *testing.T) {
	e := &StrategyEdgeEnvelope{Strategies: []StrategyEdgeItem{{Name: "unknown", Verdict: "unknown"}}}
	if s := strategyEdgeSummary(e); !strings.Contains(s, "unknown") || !strings.Contains(s, "勝 0050 偏移 無") {
		t.Fatal(s)
	}
	for _, q := range []*fakeEdgeQuerier{{err: errors.New("private")}, {}} {
		ts := &strategyEdgeToolset{q: q, logf: func(string, ...any) {}}
		_, _, err := ts.strategyEdge(t.Context(), nil, struct{}{})
		if err == nil || err.Error() != errInternal {
			t.Fatalf("err = %v", err)
		}
	}
	assertToolRegisteredReadOnly(t, &fakeEdgeQuerier{}, "get_strategy_edge")
}
