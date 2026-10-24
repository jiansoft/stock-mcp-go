package stock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeDeepDiveQuerier struct {
	fakeQuerier
	result *StockDeepDive
	err    error
	calls  int
	symbol string
}

func (f *fakeDeepDiveQuerier) AnalyzeStockDeepDive(_ context.Context, symbol string) (*StockDeepDive, error) {
	f.calls++
	f.symbol = symbol
	return f.result, f.err
}

// 透過真正的 MCP 連線驗證工具註冊、參數、文字及 structuredContent。
func deepDiveTestSession(t *testing.T, reader Querier, logf func(string, ...any)) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	AddTools(server, reader, logf)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestDeepDiveMCPPartialResultKeepsDataAndLogsPrivateFailure(t *testing.T) {
	reader := &fakeDeepDiveQuerier{result: &StockDeepDive{
		DataKind: "stock_deep_dive", StockSymbol: "00631L", Disclaimer: AnalysisDisclaimer,
		Sections: map[string]json.RawMessage{"profile": json.RawMessage(`{"stock_symbol":"00631L"}`), "valuation": json.RawMessage(`{"data_as_of":"2026-10-23","pe":12.5}`)},
		Warnings: map[string]string{"chip": "此區塊目前無法取得"},
		failures: map[string]error{"chip": errors.New("SQL internal secret")},
	}}
	var logs []string
	session := deepDiveTestSession(t, reader, func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
	found := false
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "analyze_stock_deep_dive" {
			found = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
	}
	if !found {
		t.Fatal("缺少唯讀深度分析工具")
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "analyze_stock_deep_dive", Arguments: map[string]any{"symbol": " 00631l "}})
	if err != nil || result.IsError {
		t.Fatalf("查詢失敗:%v %+v", err, result)
	}
	if reader.calls != 1 || reader.symbol != "00631L" {
		t.Fatalf("代號沒有正規化:%+v", reader)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "取得 2 個資料區塊，1 個區塊未能取得") || !strings.Contains(text, AnalysisDisclaimer) {
		t.Fatalf("摘要不符:%s", text)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out StockDeepDive
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sections) != 2 || out.Warnings["chip"] != "此區塊目前無法取得" || !strings.Contains(string(out.Sections["valuation"]), "2026-10-23") {
		t.Fatalf("遺失部分成功結果:%s", raw)
	}
	if strings.Contains(text, "secret") || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "failures") {
		t.Fatal("內部錯誤外洩")
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "00631L 區塊 chip") || !strings.Contains(logs[0], "SQL internal secret") {
		t.Fatalf("沒有保留可追查的錯誤:%v", logs)
	}
}

func TestDeepDiveMCPErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name        string
		symbol      string
		err         error
		want        string
		calls, logs int
	}{
		{"空白代號", " ", nil, "symbol", 0, 0},
		{"查無股票", "2330", fmt.Errorf("lookup: %w", ErrStockNotFound), "查無股票 2330", 1, 0},
		{"逾時", "2330", fmt.Errorf("request: %w", context.DeadlineExceeded), "查詢逾時或已取消", 1, 1},
		{"取消", "2330", fmt.Errorf("request: %w", context.Canceled), "查詢逾時或已取消", 1, 1},
		{"上游錯誤", "2330", errors.New("SQL internal secret"), errInternal, 1, 1},
		{"空結果", "2330", nil, errInternal, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeDeepDiveQuerier{err: tc.err}
			var logs []string
			session := deepDiveTestSession(t, reader, func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "analyze_stock_deep_dive", Arguments: map[string]any{"symbol": tc.symbol}})
			if err != nil || !result.IsError {
				t.Fatalf("應回傳 MCP 工具錯誤:%v %+v", err, result)
			}
			text := result.Content[0].(*mcp.TextContent).Text
			if !strings.Contains(text, tc.want) || strings.Contains(text, "secret") {
				t.Fatalf("錯誤分類或遮罩不符:%s", text)
			}
			if reader.calls != tc.calls || len(logs) != tc.logs {
				t.Fatalf("呼叫／日誌數量不符: calls=%d logs=%v", reader.calls, logs)
			}
			if tc.err != nil && tc.logs > 0 && !strings.Contains(logs[0], tc.err.Error()) {
				t.Fatalf("日誌遺失錯誤原因:%v", logs)
			}
		})
	}
}

func TestDeepDiveProfileFailureStopsBeforeFanout(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); http.Error(w, "SQL internal secret", 500) }))
	defer upstream.Close()
	result, err := NewAPIClient(upstream.URL, "", time.Second).AnalyzeStockDeepDive(t.Context(), "2330")
	if err == nil || result != nil || calls.Load() != 1 {
		t.Fatalf("profile 失敗後仍執行分析: result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}
