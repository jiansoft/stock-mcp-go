// Package stock 測試 get_holding_chip 的 API client、輸入驗證、摘要與註冊。
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

// fakeHoldingChipQuerier 是同時具備基本 Querier 與 HoldingChipQuerier 的假資料來源。
type fakeHoldingChipQuerier struct {
	fakeQuerier
	envelope *HoldingChipEnvelope
	err      error
	got      string
}

func (f *fakeHoldingChipQuerier) HoldingChip(_ context.Context, date string) (*HoldingChipEnvelope, error) {
	f.got = date
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleHoldingChip 產生一檔達標(外資大量、連買、主力)與一檔未達標的持股。
func sampleHoldingChip() *HoldingChipEnvelope {
	return &HoldingChipEnvelope{
		Date: ptr("2026-10-20"), PreviousDate: ptr("2026-10-17"), Notable: 1,
		Items: []HoldingChipItem{
			{
				StockSymbol: "1101", Name: "台泥", ClosingPrice: ptr(25.5), Volume: i64(8_000),
				ForeignNet: i64(-120), TrustNet: i64(0), DealerNet: i64(3),
			},
			{
				StockSymbol: "2330", Name: "台積電", ClosingPrice: ptr(1450.0), Volume: i64(30_000),
				ForeignNet: i64(12_000), TrustNet: i64(-300), DealerNet: i64(-50),
				ForeignShare: ptr(40.0), TrustShare: ptr(-1.0), ForeignStreak: 6, TrustStreak: -2,
				MarginBalance: i64(20_000), MarginChange: i64(-600), ShortBalance: i64(500), ShortChange: i64(10),
				Broker: &HoldingChipBroker{
					MainNet: 9_000, MainShare: 30,
					Buyers:  []HoldingChipBrokerNet{{Name: "摩根大通", Buy: 6_000, Sell: 100, Net: 5_900, Share: 19.67}},
					Sellers: []HoldingChipBrokerNet{{Name: "凱基台北", Buy: 10, Sell: 410, Net: 400, Share: 1.33}},
				},
				Reasons: []string{"foreign_heavy", "foreign_streak", "broker", "future_code"},
			},
		},
	}
}

// TestAPIClientHoldingChip 驗證路徑、參數、欄位解析、空清單與錯誤。
func TestAPIClientHoldingChip(t *testing.T) {
	var gotPath, gotRaw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRaw = r.URL.Path, r.URL.RawQuery
		switch r.URL.Query().Get("date") {
		case "2000-01-01":
			w.WriteHeader(http.StatusInternalServerError)
		case "1990-01-01":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"date":null,"previous_date":null,"next_date":null,"notable":0,"items":null}`))
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"date":"2026-10-20","previous_date":"2026-10-17","next_date":null,"notable":1,"items":[` +
				`{"stock_symbol":"2330","name":"台積電","closing_price":1450,"volume":30000,"foreign_net":12000,"trust_net":null,` +
				`"dealer_net":-50,"foreign_share":40,"trust_share":null,"foreign_streak":6,"trust_streak":0,"margin_balance":20000,` +
				`"margin_change":-600,"short_balance":null,"short_change":null,"broker":{"main_net":9000,"main_share":30,` +
				`"buyers":[{"name":"摩根大通","buy":6000,"sell":100,"net":5900,"share":19.67}],"sellers":[]},"reasons":["foreign_heavy"]}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.HoldingChip(t.Context(), "")
	if err != nil || envelope.Date == nil || *envelope.Date != "2026-10-20" || envelope.NextDate != nil || len(envelope.Items) != 1 {
		t.Fatalf("HoldingChip() = %#v, %v", envelope, err)
	}
	item := envelope.Items[0]
	if item.TrustNet != nil || *item.ForeignNet != 12_000 || item.Broker == nil || item.Broker.Buyers[0].Net != 5_900 || item.Reasons[0] != "foreign_heavy" {
		t.Fatalf("欄位解析錯誤:%#v", item)
	}
	if gotPath != "/api/v1/portfolio/chip" || gotRaw != "" {
		t.Fatalf("未指定日期不應帶參數:%s?%s", gotPath, gotRaw)
	}
	empty, err := client.HoldingChip(t.Context(), "1990-01-01")
	if err != nil || gotRaw != "date=1990-01-01" || empty.Date != nil || empty.Items == nil {
		t.Fatalf("沒有資料時 items 應是空清單:%#v, %v, %s", empty, err, gotRaw)
	}
	if _, err := client.HoldingChip(t.Context(), "2000-01-01"); err == nil {
		t.Fatal("500 應回錯誤")
	}
}

// TestHoldingChipTool 驗證輸入驗證、結構化輸出、摘要與錯誤轉換。
func TestHoldingChipTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeHoldingChipQuerier) *holdingChipToolset {
		return &holdingChipToolset{hq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}
	f := &fakeHoldingChipQuerier{envelope: sampleHoldingChip()}
	result, output, err := newToolset(f).holdingChip(t.Context(), nil, HoldingChipInput{Date: "2026-10-20"})
	if err != nil || f.got != "2026-10-20" {
		t.Fatalf("holdingChip 不應失敗:%v, %q", err, f.got)
	}
	if out, ok := output.(HoldingChipOutput); !ok || out.DataKind != "holding_chip" || out.IsRealtime || out.Notable != 1 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"持股籌碼日報 2026-10-20(前一交易日 2026-10-17、後一交易日 無):共 2 檔持股(普通股,不含 ETF),1 檔達通知門檻。",
		"2330 台積電(收盤 1450,成交 30000 張)〔外資大量、外資連續、主力、future_code〕:" +
			"外資 +12000 張(佔量 40%,連買 6 日)、投信 -300 張(佔量 -1%,連賣 2 日)、自營 -50 張;" +
			"融資餘額 20000(-600 張)、融券餘額 500(+10 張);主力 +9000 張(佔量 30%),買超分點 摩根大通 5900 張,賣超分點 凱基台北 400 張。",
		"未達門檻(張):1101 台泥 外資 -120 張/投信 0 張。",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	if _, _, err := newToolset(f).holdingChip(t.Context(), nil, HoldingChipInput{Date: "2026-13-01"}); err == nil {
		t.Error("不合法日期應回錯誤")
	}
	if _, _, err := newToolset(&fakeHoldingChipQuerier{err: errors.New("boom")}).holdingChip(t.Context(), nil, HoldingChipInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Errorf("內部錯誤應記錄一次:%v", logged)
	}
	if summary := holdingChipSummary(&HoldingChipEnvelope{Items: []HoldingChipItem{}}); summary != "查無持股籌碼資料。" {
		t.Errorf("沒有資料的摘要錯誤:%s", summary)
	}
	quiet := HoldingChipItem{StockSymbol: "2603", Name: "長榮", Reasons: []string{"margin"}}
	if text := holdingChipItemText(quiet); !strings.Contains(text, "外資 無、投信 無、自營 無資料 張") || strings.Contains(text, "主力") {
		t.Errorf("缺資料的持股文字錯誤:%s", text)
	}
	if date := holdingChipSchema().Properties["date"]; date == nil || date.Default != nil {
		t.Error("date 不該有 schema 預設值")
	}
}

// TestHoldingChipToolIsRegisteredReadOnly 驗證只有具備 HoldingChipQuerier 時才註冊,且為唯讀。
func TestHoldingChipToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeHoldingChipQuerier{}, nil)
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
		if tool.Name == "get_holding_chip" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_holding_chip 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Fatal("具備 HoldingChipQuerier 時應註冊 get_holding_chip")
	}
}
