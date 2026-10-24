package stock

import (
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHoldingHistoryAliasPreservesFullPeriods(t *testing.T) {
	f := &fakeEtfHoldingQuerier{history: &EtfHoldingHistoryEnvelope{StockSymbol: "0056", Periods: []EtfHoldingPeriodItem{{AsOf: "2026-10-01", Holdings: []EtfHoldingItem{{Symbol: "2330", Name: "台積電", Weight: 12.5}}}}}}
	tool := &etfHoldingChangesToolset{hq: f, logf: func(string, ...any) {}}
	result, output, err := tool.etfHoldingHistory(t.Context(), nil, EtfHoldingChangesInput{Symbol: " 0056 ", Periods: 24})
	if err != nil || f.gotPeriods != 24 || f.gotSymbol != "0056" {
		t.Fatalf("history alias: %v %+v", err, f)
	}
	data := output.(EtfHoldingChangesOutput)
	if data.DataKind != "etf_holding_history" || data.Periods[0].Holdings[0].Weight != 12.5 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "12.5%") {
		t.Fatalf("missing history: %+v", output)
	}
	if _, _, err = tool.etfHoldingHistory(t.Context(), nil, EtfHoldingChangesInput{Symbol: "0056", Periods: 25}); err == nil {
		t.Fatal("accepted invalid periods")
	}
}

func TestEtfProxyTrackingToolContract(t *testing.T) {
	status := 200
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/etfs/00631L/tracking-error" || r.Header.Get("Authorization") != "Bearer private" {
			t.Errorf("wrong upstream: %s", r.URL.Path)
		}
		w.WriteHeader(status)
		if status != 200 {
			_, _ = w.Write([]byte("SQL secret"))
			return
		}
		_, _ = w.Write([]byte(`{"stock_symbol":"00631L","benchmark_symbol":"0050","benchmark_kind":"etf_proxy","target_benchmark":"原指數","data_as_of":null,"samples":30,"tracking_error_percent":null,"tracking_difference_percent":null,"note":"代理基準報酬差異，非實際指數追蹤誤差"}`))
	}))
	defer upstream.Close()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	AddTools(server, NewAPIClient(upstream.URL, "private", time.Second), nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), t1, nil); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(t.Context(), t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_etf_proxy_tracking", Arguments: map[string]any{"symbol": " 00631l "}})
	if err != nil || result.IsError {
		t.Fatalf("tool failed: %v %+v", err, result)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(raw), `"tracking_error_percent":null`) || !strings.Contains(string(raw), `"benchmark_kind":"etf_proxy"`) || !strings.Contains(string(raw), `"data_kind":"etf_proxy_tracking"`) {
		t.Fatalf("bad contract: %s", raw)
	}
	for _, code := range []int{404, 500} {
		status = code
		result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_etf_proxy_tracking", Arguments: map[string]any{"symbol": "00631l"}})
		if err != nil || !result.IsError {
			t.Fatalf("expected tool error %d: %v", code, err)
		}
		if strings.Contains(result.Content[0].(*mcp.TextContent).Text, "secret") {
			t.Fatal("leaked secret")
		}
	}
	for _, symbol := range []string{"../", " "} {
		result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_etf_proxy_tracking", Arguments: map[string]any{"symbol": symbol}})
		if err != nil || !result.IsError {
			t.Fatalf("accepted invalid symbol %q: %v", symbol, err)
		}
	}
}

func TestHoldingHistoryAliasEmptyPeriods(t *testing.T) {
	for _, tc := range []struct {
		name    string
		periods []EtfHoldingPeriodItem
		want    string
	}{
		{"沒有歷史", nil, "沒有歷史紀錄"},
		{"當期沒有持股", []EtfHoldingPeriodItem{{AsOf: "2026-10-01", Holdings: []EtfHoldingItem{}}}, "2026-10-01:沒有持股紀錄"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeEtfHoldingQuerier{history: &EtfHoldingHistoryEnvelope{StockSymbol: "0056", Periods: tc.periods}}
			tool := &etfHoldingChangesToolset{hq: reader, logf: func(string, ...any) {}}
			result, output, err := tool.etfHoldingHistory(t.Context(), nil, EtfHoldingChangesInput{Symbol: "0056"})
			if err != nil {
				t.Fatal(err)
			}
			text := result.Content[0].(*mcp.TextContent).Text
			if !strings.Contains(text, tc.want) || !strings.Contains(text, AnalysisDisclaimer) {
				t.Fatalf("空資料摘要不符:%s", text)
			}
			out := output.(EtfHoldingChangesOutput)
			if out.DataKind != "etf_holding_history" || len(out.Periods) != len(tc.periods) {
				t.Fatalf("空資料契約不符:%+v", out)
			}
		})
	}
}
