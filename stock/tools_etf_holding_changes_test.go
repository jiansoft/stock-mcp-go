// Package stock 測試 get_etf_holding_changes 與 get_portfolio_etf_exposure 的 API client、輸入驗證、摘要與註冊。
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

// fakeEtfHoldingQuerier 是同時具備基本 Querier、ETF 持股歷史與持股穿透的假資料來源。
type fakeEtfHoldingQuerier struct {
	fakeQuerier
	history     *EtfHoldingHistoryEnvelope
	exposure    *PortfolioEtfExposureEnvelope
	err         error
	gotSymbol   string
	gotPeriods  int
	gotMemberID int64
	gotLimit    int
}

func (f *fakeEtfHoldingQuerier) EtfHoldingHistory(_ context.Context, symbol string, periods int) (*EtfHoldingHistoryEnvelope, error) {
	f.gotSymbol, f.gotPeriods = symbol, periods
	return f.history, f.err
}

func (f *fakeEtfHoldingQuerier) PortfolioEtfExposure(_ context.Context, memberID int64, limit int) (*PortfolioEtfExposureEnvelope, error) {
	f.gotMemberID, f.gotLimit = memberID, limit
	return f.exposure, f.err
}

// TestAPIClientEtfHoldingHistory 驗證路徑與參數、404 回 nil、清單防禦。
func TestAPIClientEtfHoldingHistory(t *testing.T) {
	var gotPath, gotRaw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRaw = r.URL.Path, r.URL.RawQuery
		if strings.Contains(r.URL.Path, "9999") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"stock_symbol":"0056","name":"元大高股息","periods":[` +
			`{"as_of":"2026-10-01","holdings":null,"changes":{"previous_as_of":"2026-09-01","added":null,"removed":null,"moved":null}},` +
			`{"as_of":"2026-09-01","holdings":null,"changes":null}]}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.EtfHoldingHistory(t.Context(), "0056", 2)
	if err != nil || gotPath != "/api/v1/etfs/0056/holdings/history" || gotRaw != "periods=2" {
		t.Fatalf("EtfHoldingHistory() = %#v, %v, %s?%s", envelope, err, gotPath, gotRaw)
	}
	first := envelope.Periods[0]
	if first.Holdings == nil || first.Changes.Added == nil || first.Changes.Removed == nil || first.Changes.Moved == nil {
		t.Fatalf("清單應補成空陣列:%#v", first)
	}
	if envelope.Periods[1].Changes != nil {
		t.Fatalf("沒有前一期時 changes 應維持 nil:%#v", envelope.Periods[1])
	}
	if _, err := client.EtfHoldingHistory(t.Context(), "0056", 0); err != nil || gotRaw != "" {
		t.Fatalf("periods 為 0 不送參數:%v %q", err, gotRaw)
	}
	if missing, err := client.EtfHoldingHistory(t.Context(), "9999", 0); err != nil || missing != nil {
		t.Fatalf("404 應回 nil, nil:%#v, %v", missing, err)
	}
}

// TestEtfHoldingChangesTool 驗證代號正規化、範圍驗證、摘要、查無與錯誤轉換。
func TestEtfHoldingChangesTool(t *testing.T) {
	toolset := func(f *fakeEtfHoldingQuerier) *etfHoldingChangesToolset {
		return &etfHoldingChangesToolset{hq: f, logf: func(string, ...any) {}}
	}
	moved := make([]EtfHoldingMoveItem, 0, 7)
	for i := range 7 {
		moved = append(moved, EtfHoldingMoveItem{Label: "股" + string(rune('A'+i)), Before: 1, After: 2})
	}
	f := &fakeEtfHoldingQuerier{history: &EtfHoldingHistoryEnvelope{
		StockSymbol: "0056", Name: "元大高股息",
		Periods: []EtfHoldingPeriodItem{
			{AsOf: "2026-10-01", Holdings: []EtfHoldingItem{{Symbol: "2454", Name: "聯發科", Weight: 8.46}, {Name: "現金", Weight: 1}},
				Changes: &EtfHoldingChanges{PreviousAsOf: "2026-09-01",
					Added:   []EtfHoldingWeightItem{{Label: "2303 聯電", Weight: 4}},
					Removed: []EtfHoldingWeightItem{{Label: "2412 中華電", Weight: 3.2}},
					Moved:   moved}},
			{AsOf: "2026-09-01", Changes: &EtfHoldingChanges{PreviousAsOf: "2026-08-01", Added: []EtfHoldingWeightItem{}, Removed: []EtfHoldingWeightItem{}, Moved: []EtfHoldingMoveItem{}}},
			{AsOf: "2026-08-01"},
		},
	}}
	result, output, err := toolset(f).etfHoldingChanges(t.Context(), nil, EtfHoldingChangesInput{Symbol: " 0056 ", Periods: 3})
	if err != nil || f.gotSymbol != "0056" || f.gotPeriods != 3 {
		t.Fatalf("參數應正規化:%v %q %d", err, f.gotSymbol, f.gotPeriods)
	}
	if out, ok := output.(EtfHoldingChangesOutput); !ok || out.DataKind != "etf_holding_changes" || len(out.Periods) != 3 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"0056 元大高股息 最新一期(2026-10-01)前十大持股:2454 聯發科 8.46%、現金 1%。",
		"2026-10-01(相對 2026-09-01):新進 2303 聯電 4%;剔除 2412 中華電 3.2%;權重變化 股A 1%→2%、股B 1%→2%、股C 1%→2%、股D 1%→2%、股E 1%→2% 等 7 檔。",
		"2026-09-01(相對 2026-08-01):前十大與權重都沒有變。",
		"2026-08-01:沒有更早一期的紀錄可比較。",
		"免責聲明",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}

	empty := &fakeEtfHoldingQuerier{history: &EtfHoldingHistoryEnvelope{StockSymbol: "00632R", Name: "元大台灣50反1", Periods: []EtfHoldingPeriodItem{}}}
	result, _, err = toolset(empty).etfHoldingChanges(t.Context(), nil, EtfHoldingChangesInput{Symbol: "00632R"})
	if err != nil || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "沒有前十大持股紀錄") {
		t.Fatalf("沒有持股時應說明:%v", err)
	}
	for _, in := range []EtfHoldingChangesInput{{Symbol: ""}, {Symbol: "0056", Periods: 25}, {Symbol: "0056", Periods: -1}} {
		if _, _, err := toolset(f).etfHoldingChanges(t.Context(), nil, in); err == nil {
			t.Errorf("%#v 應回錯誤", in)
		}
	}
	if _, _, err := toolset(&fakeEtfHoldingQuerier{}).etfHoldingChanges(t.Context(), nil, EtfHoldingChangesInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "查無 ETF 9999") {
		t.Fatalf("查無應提示:%v", err)
	}
	if _, _, err := toolset(&fakeEtfHoldingQuerier{err: errors.New("boom")}).etfHoldingChanges(t.Context(), nil, EtfHoldingChangesInput{Symbol: "0056"}); err == nil || strings.Contains(err.Error(), "boom") {
		t.Fatalf("上游錯誤不應外洩:%v", err)
	}
}

// TestAPIClientPortfolioEtfExposure 驗證參數組裝與清單防禦。
func TestAPIClientPortfolioEtfExposure(t *testing.T) {
	var gotRaw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.RawQuery
		if r.URL.Path != "/api/v1/portfolio/etf-exposure" {
			t.Errorf("路徑錯誤:%s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"total_value":1,"etf_value":0,"uncovered_value":0,"total_items":1,"etfs":null,"items":[{"symbol":"2330","sources":null}],"unpriced":null}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.PortfolioEtfExposure(t.Context(), 3, 10)
	if err != nil || gotRaw != "limit=10&member_id=3" {
		t.Fatalf("PortfolioEtfExposure() = %#v, %v, %s", envelope, err, gotRaw)
	}
	if envelope.Etfs == nil || envelope.Unpriced == nil || envelope.Items[0].Sources == nil {
		t.Fatalf("清單應補成空陣列:%#v", envelope)
	}
	if _, err := client.PortfolioEtfExposure(t.Context(), 0, 0); err != nil || gotRaw != "member_id=0" {
		t.Fatalf("limit 為 0 不送參數:%v %q", err, gotRaw)
	}
}

// TestPortfolioEtfExposureTool 驗證輸入驗證、摘要與錯誤轉換。
func TestPortfolioEtfExposureTool(t *testing.T) {
	toolset := func(f *fakeEtfHoldingQuerier) *portfolioEtfExposureToolset {
		return &portfolioEtfExposureToolset{eq: f, logf: func(string, ...any) {}}
	}
	f := &fakeEtfHoldingQuerier{exposure: &PortfolioEtfExposureEnvelope{
		TotalValue: 3.6e8, EtfValue: 2.6e8, UncoveredValue: 1.31e8, TotalItems: 3,
		Etfs: []ExposureEtfItem{
			{StockSymbol: "0050", Name: "元大台灣50", MarketValue: 2e8, HoldingsAsOf: ptr("2026-09-30"), CoveredWeight: 62},
			{StockSymbol: "00679B", Name: "元大美債20年", MarketValue: 1e7},
		},
		Items: []ExposureItem{
			{Symbol: "2330", Name: "台積電", DirectValue: 1e8, EtfValue: 1.12e8, TotalValue: 2.12e8, Weight: 58.89,
				Sources: []ExposureSourceItem{{EtfSymbol: "0050", Value: 1.12e8}}},
			{Name: "現金", TotalValue: 1e6, Weight: 0.28, Sources: []ExposureSourceItem{{EtfSymbol: "0056", Value: 1e6}}},
		},
		Unpriced: []string{"2887Z1"},
	}}
	result, output, err := toolset(f).portfolioEtfExposure(t.Context(), nil, PortfolioEtfExposureInput{MemberID: 2, Limit: 2})
	if err != nil || f.gotMemberID != 2 || f.gotLimit != 2 {
		t.Fatalf("參數應原樣傳遞:%v %d %d", err, f.gotMemberID, f.gotLimit)
	}
	if out, ok := output.(PortfolioEtfExposureOutput); !ok || out.DataKind != "portfolio_etf_exposure" || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"持股總市值 3.6 億,其中 ETF 2.6 億;ETF 前十大以外無法拆解 1.3 億。",
		"持有 ETF:0050 元大台灣50 2 億(前十大 62%,2026-09-30)、00679B 元大美債20年 1000 萬(沒有前十大持股,無法拆解)。",
		"合計曝險前 2 大(共 3 檔):2330 台積電 2.1 億(58.89%;直接 1 億,經由 0050 1.1 億);現金 100 萬(0.28%;直接 0 元,經由 0056 100 萬)。",
		"沒有收盤價、未列入:2887Z1。",
		"免責聲明",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}

	if got := moneyText(3678.4); got != "3678 元" {
		t.Errorf("不到 1 萬應以元顯示:%s", got)
	}
	none := &fakeEtfHoldingQuerier{exposure: &PortfolioEtfExposureEnvelope{}}
	result, _, err = toolset(none).portfolioEtfExposure(t.Context(), nil, PortfolioEtfExposureInput{})
	if err != nil || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "目前沒有可計算市值的持股") {
		t.Fatalf("沒有持股時應說明:%v", err)
	}
	for _, in := range []PortfolioEtfExposureInput{{MemberID: -1}, {Limit: 201}, {Limit: -1}} {
		if _, _, err := toolset(f).portfolioEtfExposure(t.Context(), nil, in); err == nil {
			t.Errorf("%#v 應回錯誤", in)
		}
	}
	if _, _, err := toolset(&fakeEtfHoldingQuerier{err: errors.New("boom")}).portfolioEtfExposure(t.Context(), nil, PortfolioEtfExposureInput{}); err == nil || strings.Contains(err.Error(), "boom") {
		t.Fatalf("上游錯誤不應外洩:%v", err)
	}
}

// TestEtfHoldingToolsAreRegisteredReadOnly 驗證具備對應介面時註冊兩個唯讀工具。
func TestEtfHoldingToolsAreRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeEtfHoldingQuerier{}, nil)
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
	found := map[string]bool{}
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if tool.Name == "get_etf_holding_changes" || tool.Name == "get_portfolio_etf_exposure" {
			found[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
	}
	if !found["get_etf_holding_changes"] || !found["get_portfolio_etf_exposure"] {
		t.Fatalf("應註冊兩個唯讀工具:%v", found)
	}
}
