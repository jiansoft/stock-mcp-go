// Package stock 測試 get_holder_levels 的 API client、輸入驗證、摘要與註冊。
package stock

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func sampleHolderLevels() *HolderLevelsEnvelope {
	return &HolderLevelsEnvelope{
		StockSymbol: "2330",
		Date:        "2026-10-22",
		Levels: []HolderLevel{
			{Level: 1, Label: "1～999 股", Holders: 1_960_000, Shares: 400_000_000, Percent: ptr(1.54)},
			{Level: 15, Label: "1,000,001 股以上", Holders: 1476, Shares: 21_900_000_000, Percent: ptr(84.42)},
		},
		Adjustment: &HolderLevel{Level: 16, Label: "差異數調整", Holders: 2},
		Total:      &HolderLevel{Level: 17, Label: "合計", Holders: 3_010_913, Shares: 25_930_000_000, Percent: ptr(100.0)},
	}
}

// TestAPIClientHolderLevels 驗證路徑、date 參數、404 語意與空陣列補齊。
func TestAPIClientHolderLevels(t *testing.T) {
	var gotQueries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/2330/holder-levels":
			gotQueries = append(gotQueries, r.URL.RawQuery)
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","date":"2026-10-22","levels":null,"adjustment":null,"total":{"level":17,"label":"合計","holders":3010913,"shares":25930000000,"percent":100}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	levels, err := client.HolderLevels(t.Context(), "2330", "")
	if err != nil || levels.Levels == nil || levels.Adjustment != nil || levels.Total == nil || levels.Total.Holders != 3_010_913 {
		t.Fatalf("HolderLevels() = %#v, %v", levels, err)
	}
	if _, err := client.HolderLevels(t.Context(), "2330", "2026-10-30"); err != nil {
		t.Fatalf("帶 date 不應失敗:%v", err)
	}
	if strings.Join(gotQueries, "|") != "|date=2026-10-30" {
		t.Fatalf("query string 錯誤:%q", gotQueries)
	}
	if _, err := client.HolderLevels(t.Context(), "9999", ""); !errors.Is(err, ErrHolderLevelsNotFound) {
		t.Fatalf("404 應回 ErrHolderLevelsNotFound:%v", err)
	}
	unreachable := NewAPIClient("http://127.0.0.1:1", "secret", time.Second)
	if _, err := unreachable.HolderLevels(t.Context(), "2330", ""); err == nil || errors.Is(err, ErrHolderLevelsNotFound) {
		t.Fatalf("連線失敗應回一般錯誤:%v", err)
	}
}

// TestHolderLevelsTool 驗證日期檢查、錯誤分層、摘要與 structured output。
func TestHolderLevelsTool(t *testing.T) {
	newToolset := func(f *fakeChipQuerier) *chipToolset {
		return &chipToolset{chips: f, logf: func(string, ...any) {}}
	}

	f := &fakeChipQuerier{levels: sampleHolderLevels()}
	result, output, err := newToolset(f).holderLevels(t.Context(), nil, HolderLevelsInput{Symbol: "2330", Date: "2026-10-23"})
	if err != nil {
		t.Fatalf("holderLevels 不應失敗:%v", err)
	}
	if f.gotDate != "2026-10-23" {
		t.Fatalf("date 應原樣傳給 Data API,收到 %q", f.gotDate)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"股票 2330 集保股權分散 2026-10-22:",
		"1～999 股 1960000 人、1.54%;1,000,001 股以上 1476 人、84.42%。",
		"合計 3010913 人、25930000000 股。",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(HolderLevelsOutput); !ok || out.DataKind != "holder_levels" || out.Date != "2026-10-22" || len(out.Levels) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	// 沒有合計列時不寫合計。
	noTotal := sampleHolderLevels()
	noTotal.Total = nil
	if text := holderLevelsSummary(noTotal); strings.Contains(text, "合計") {
		t.Fatalf("沒有合計時不應出現合計:%s", text)
	}

	if _, _, err := newToolset(f).holderLevels(t.Context(), nil, HolderLevelsInput{Symbol: "2330", Date: "2026-13-40"}); err == nil || !strings.Contains(err.Error(), "date") {
		t.Fatalf("不合法日期應回錯誤:%v", err)
	}
	if _, _, err := newToolset(f).holderLevels(t.Context(), nil, HolderLevelsInput{Symbol: ""}); err == nil {
		t.Fatal("空代號應回錯誤")
	}
	if _, _, err := newToolset(&fakeChipQuerier{err: ErrHolderLevelsNotFound}).holderLevels(t.Context(), nil, HolderLevelsInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "2026-10-22") {
		t.Fatalf("404 應說明分級資料起始日:%v", err)
	}
	if _, _, err := newToolset(&fakeChipQuerier{err: errors.New("boom")}).holderLevels(t.Context(), nil, HolderLevelsInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestHolderLevelsToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestHolderLevelsToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeChipQuerier{}, nil)
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
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if tool.Name == "get_holder_levels" {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatalf("get_holder_levels 應為唯讀:%#v", tool.Annotations)
			}
			if !strings.Contains(tool.Description, "2026-10-22") {
				t.Fatalf("說明應註明分級資料起始日:%s", tool.Description)
			}
			return
		}
	}
	t.Fatal("未註冊 get_holder_levels")
}
