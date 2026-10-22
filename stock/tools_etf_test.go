// Package stock 測試 screen_etfs、get_etf_detail 的 API client、輸入驗證、摘要與註冊。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeEtfQuerier 是同時具備基本 Querier 與 EtfQuerier 的假資料來源。
type fakeEtfQuerier struct {
	fakeQuerier
	list      *EtfListEnvelope
	detail    *EtfDetailEnvelope
	err       error
	gotParams EtfScreenParams
	gotSymbol string
	gotDays   int
	compare   *EtfCompareEnvelope
	gotList   []string
}

func (f *fakeEtfQuerier) CompareEtfs(_ context.Context, symbols []string) (*EtfCompareEnvelope, error) {
	f.gotList = symbols
	return f.compare, f.err
}

func (f *fakeEtfQuerier) ScreenEtfs(_ context.Context, params EtfScreenParams) (*EtfListEnvelope, error) {
	f.gotParams = params
	return f.list, f.err
}

func (f *fakeEtfQuerier) EtfDetail(_ context.Context, symbol string, navDays int) (*EtfDetailEnvelope, error) {
	f.gotSymbol, f.gotDays = symbol, navDays
	return f.detail, f.err
}

func sampleEtfItem() EtfItem {
	return EtfItem{
		StockSymbol: "00679B", Name: "元大美債20年", Market: "tpex", Strategy: "bond", Region: "美國",
		DividendFrequency: "quarterly", Issuer: "元大投信", Benchmark: "ICE 美國政府 20+ 年期債券指數",
		QuoteDate: ptr("2026-10-22"), ClosingPrice: ptr(26.01), ChangeRange: ptr(0.93), TradeValue: ptr(8.43e8),
		Nav: ptr(26.04), PremiumRate: ptr(-0.12), PremiumAvg20d: ptr(-0.05), TotalAssets: ptr(1.603e11),
		NetFlow: ptr(-5.47e8), Dividend12m: ptr(1.12), Yield12m: ptr(4.31), Ret1y: ptr(-2.8), Holders: ptr(int64(231152)),
	}
}

// TestAPIClientEtfs 驗證清單參數組裝、明細路徑、404 與空清單。
func TestAPIClientEtfs(t *testing.T) {
	var gotPath, gotRaw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRaw = r.URL.Path, r.URL.RawQuery
		switch {
		case r.URL.Path == "/api/v1/etfs/9999":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/api/v1/etfs/0050":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"item":{"stock_symbol":"0050","name":"元大台灣50","market":"twse","strategy":"market_cap","themes":[]},` +
				`"profile":null,"performance":null,"holdings_as_of":null,"holdings":null,"industries_as_of":null,"industries":null,` +
				`"assets_as_of":null,"assets":null,"navs":null}`))
		case r.URL.Query().Get("limit") == "999":
			w.WriteHeader(http.StatusUnprocessableEntity)
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"total":0,"sort":"trade_value","order":"desc","items":null}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	list, err := client.ScreenEtfs(t.Context(), EtfScreenParams{})
	if err != nil || gotPath != "/api/v1/etfs" || gotRaw != "" || list.Items == nil {
		t.Fatalf("未指定條件不應帶參數且 items 為空清單:%#v, %v, %s?%s", list, err, gotPath, gotRaw)
	}
	if _, err = client.ScreenEtfs(t.Context(), EtfScreenParams{
		Strategies: []string{"bond", "high_dividend"}, Region: "美國", Frequency: "monthly", Market: "tpex",
		Keyword: "美債", Sort: "premium_rate", Order: "asc", Limit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	want := "frequency=monthly&keyword=%E7%BE%8E%E5%82%B5&limit=5&market=tpex&order=asc&region=%E7%BE%8E%E5%9C%8B&sort=premium_rate&strategy=bond%2Chigh_dividend"
	if gotRaw != want {
		t.Fatalf("參數組裝錯誤:%s", gotRaw)
	}
	if _, err := client.ScreenEtfs(t.Context(), EtfScreenParams{Limit: 999}); err == nil {
		t.Fatal("非 2xx 應回錯誤")
	}

	detail, err := client.EtfDetail(t.Context(), "0050", 5)
	if err != nil || detail == nil || gotRaw != "nav_days=5" || detail.Holdings == nil || detail.Industries == nil ||
		detail.Assets == nil || detail.Navs == nil {
		t.Fatalf("明細的清單欄位應為空清單:%#v, %v, %s", detail, err, gotRaw)
	}
	if _, err = client.EtfDetail(t.Context(), "0050", 0); err != nil || gotRaw != "" {
		t.Fatalf("未指定天數不應帶參數:%v, %s", err, gotRaw)
	}
	if missing, err := client.EtfDetail(t.Context(), "9999", 0); err != nil || missing != nil {
		t.Fatalf("404 應回 nil, nil:%#v, %v", missing, err)
	}
}

// TestScreenEtfsTool 驗證參數轉送、結構化輸出、摘要與錯誤轉換。
func TestScreenEtfsTool(t *testing.T) {
	var logged []string
	toolset := func(f *fakeEtfQuerier) *etfToolset {
		return &etfToolset{eq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}
	item := sampleEtfItem()
	item.NextExDate, item.NextDividend = ptr("2026-11-18"), ptr(0.28)
	f := &fakeEtfQuerier{list: &EtfListEnvelope{Total: 31, Sort: "premium_rate", Order: "asc", Items: []EtfItem{item}}}
	result, output, err := toolset(f).screenEtfs(t.Context(), nil, ScreenEtfsInput{
		Strategy: []string{"bond"}, Region: " 美國 ", Keyword: " 美債 ", Sort: "premium_rate", Order: "asc", Limit: 10,
	})
	if err != nil || f.gotParams.Region != "美國" || f.gotParams.Keyword != "美債" || f.gotParams.Limit != 10 ||
		strings.Join(f.gotParams.Strategies, ",") != "bond" {
		t.Fatalf("參數轉送錯誤:%v %#v", err, f.gotParams)
	}
	if out, ok := output.(ScreenEtfsOutput); !ok || out.DataKind != "etf_screen" || out.IsRealtime || out.Total != 31 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"符合條件的 ETF 共 31 檔,依折溢價由小到大列出 1 檔。",
		"1. 00679B 元大美債20年(債券、美國、季配、上櫃):收盤 26.01(+0.93%)、成交值 8.4 億、規模 1603 億、折溢價 -0.12%、" +
			"近 12 月殖利率 4.31%、近 1 年含息報酬 -2.80%、下次除息 2026-11-18(0.28 元)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	for _, bad := range []int{-1, 101} {
		if _, _, err := toolset(f).screenEtfs(t.Context(), nil, ScreenEtfsInput{Limit: bad}); err == nil {
			t.Errorf("limit=%d 應回錯誤", bad)
		}
	}
	if _, _, err := toolset(&fakeEtfQuerier{err: errors.New("boom")}).screenEtfs(t.Context(), nil, ScreenEtfsInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Errorf("內部錯誤應記錄一次:%v", logged)
	}
	if limit := screenEtfsSchema().Properties["limit"]; limit == nil || limit.Default != nil {
		t.Error("limit 不該有 schema 預設值")
	}
}

// TestScreenEtfsSummaryLimitsList 驗證超過上限時只列前幾檔並註明省略數。
func TestScreenEtfsSummaryLimitsList(t *testing.T) {
	e := &EtfListEnvelope{Total: 40, Sort: "unknown", Order: "desc"}
	for i := range etfScreenListed + 3 {
		e.Items = append(e.Items, EtfItem{StockSymbol: fmt.Sprintf("00%03d", i), Strategy: "thematic", Market: "twse"})
	}
	summary := screenEtfsSummary(e)
	if !strings.Contains(summary, "依unknown由大到小") || !strings.Contains(summary, "另有 3 檔見結構化輸出。") {
		t.Fatalf("清單應截斷並註明:%s", summary)
	}
	if yiText(nil) != "無" || signedPercentPtr(nil) != "無" || int64Text(nil) != "無" || orNone("") != "無" {
		t.Fatal("nil 與空值應顯示無")
	}
	if got := yiText(ptr(3.4e6)); got != "340 萬" {
		t.Fatalf("不到 1 億應以萬元顯示:%s", got)
	}
	if got := yiText(ptr(-1.25e8)); got != "-1.3 億" {
		t.Fatalf("1 億以上以億元顯示:%s", got)
	}
}

// TestEtfDetailTool 驗證代號正規化、範圍檢查、查無資料、摘要與錯誤轉換。
func TestEtfDetailTool(t *testing.T) {
	toolset := func(f *fakeEtfQuerier) *etfToolset {
		return &etfToolset{eq: f, logf: func(string, ...any) {}}
	}
	detail := &EtfDetailEnvelope{
		Item:    sampleEtfItem(),
		Profile: &EtfProfileInfo{FullName: "元大美國政府20年期(以上)債券證券投資信託基金", ManagementFee: "0.15%"},
		Performance: &EtfPerformanceInfo{
			CalcDate: ptr("2026-10-22"), Ret1y: ptr(-2.8), Vol1y: ptr(12.3), Mdd1y: ptr(-9.1), Beta1y: ptr(0.1), Sharpe1y: ptr(-0.2),
			NavCalcDate: ptr("2026-10-22"), NavRet1y: ptr(-2.9),
		},
		HoldingsAsOf:   ptr("2026-09-30"),
		Holdings:       []EtfHoldingItem{{Name: "US TREASURY N/B 4.75% 05/15/2055", Weight: 5.28}},
		IndustriesAsOf: ptr("2026-10-17"),
		Industries:     []EtfAllocationItem{{Label: "政府公債", Weight: 98.1}},
		Navs: []EtfNavPoint{
			{Date: "2026-10-22", PremiumRate: ptr(-0.12)}, {Date: "2026-10-21", PremiumRate: ptr(0.3)}, {Date: "2026-10-20"},
		},
	}
	f := &fakeEtfQuerier{detail: detail}
	result, output, err := toolset(f).etfDetail(t.Context(), nil, EtfDetailInput{Symbol: " 00679b ", NavDays: 3})
	if err != nil || f.gotSymbol != "00679B" || f.gotDays != 3 {
		t.Fatalf("代號應正規化為大寫:%v %s %d", err, f.gotSymbol, f.gotDays)
	}
	if out, ok := output.(EtfDetailOutput); !ok || out.DataKind != "etf_detail" || out.Item.StockSymbol != "00679B" {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"00679B 元大美債20年(債券、美國、季配、上櫃),全名 元大美國政府20年期(以上)債券證券投資信託基金;發行投信 元大投信",
		"經理費 0.15%、保管費 無",
		"折溢價 -0.12%(近 20 筆平均 -0.05%)、規模 1603 億、今日淨申購 -5.5 億、受益人數 231152",
		"市價含息報酬(2026-10-22):1 月 無、今年 無、1 年 -2.80%",
		"淨值含息報酬(2026-10-22):1 月 無、今年 無、1 年 -2.90%",
		"前十大持股(2026-09-30):US TREASURY N/B 4.75% 05/15/2055 5.28%。",
		"行業比重(2026-10-17):政府公債 98.1%。",
		"近 3 個交易日折溢價介於 -0.12% 至 +0.30%。",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	if strings.Contains(text, "資產分佈") {
		t.Error("沒有資產分佈時不應出現該段")
	}
	bare := etfDetailSummary(&EtfDetailEnvelope{Item: EtfItem{StockSymbol: "0050", Market: "twse"}})
	if strings.Contains(bare, "市價含息報酬") || strings.Contains(bare, "前十大持股") || strings.Contains(bare, "折溢價介於") {
		t.Fatalf("沒有績效、持股與淨值時不應出現該段:%s", bare)
	}

	if _, _, err := toolset(&fakeEtfQuerier{}).etfDetail(t.Context(), nil, EtfDetailInput{Symbol: "9999"}); err == nil ||
		!strings.Contains(err.Error(), "查無 ETF 9999") {
		t.Fatalf("查無資料應提示改用 screen_etfs:%v", err)
	}
	for _, in := range []EtfDetailInput{{Symbol: ""}, {Symbol: "0050", NavDays: 61}, {Symbol: "0050", NavDays: -1}} {
		if _, _, err := toolset(f).etfDetail(t.Context(), nil, in); err == nil {
			t.Errorf("%#v 應回錯誤", in)
		}
	}
	if _, _, err := toolset(&fakeEtfQuerier{err: errors.New("boom")}).etfDetail(t.Context(), nil, EtfDetailInput{Symbol: "0050"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestEtfToolsAreRegisteredReadOnly 驗證只有具備介面時才註冊,且為唯讀。
func TestEtfToolsAreRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeEtfQuerier{}, nil)
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
		if tool.Name == "screen_etfs" || tool.Name == "get_etf_detail" || tool.Name == "compare_etfs" {
			found[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
	}
	if !found["screen_etfs"] || !found["get_etf_detail"] || !found["compare_etfs"] {
		t.Fatalf("具備 EtfQuerier 時應註冊三個唯讀工具:%v", found)
	}
}

// TestAPIClientCompareEtfs 驗證路徑與參數、404 回 nil、清單防禦。
func TestAPIClientCompareEtfs(t *testing.T) {
	var gotRaw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.RawQuery
		if r.URL.Path != "/api/v1/etfs/compare" {
			t.Errorf("路徑錯誤:%s", r.URL.Path)
		}
		if strings.Contains(r.URL.Query().Get("symbols"), "9999") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"items":null,"overlaps":[{"a":"0050","b":"006208","common":null,"overlap_weight":0}]}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	envelope, err := client.CompareEtfs(t.Context(), []string{"0050", "006208"})
	if err != nil || gotRaw != "symbols=0050%2C006208" || envelope.Items == nil || envelope.Overlaps[0].Common == nil {
		t.Fatalf("CompareEtfs() = %#v, %v, %s", envelope, err, gotRaw)
	}
	if missing, err := client.CompareEtfs(t.Context(), []string{"0050", "9999"}); err != nil || missing != nil {
		t.Fatalf("404 應回 nil, nil:%#v, %v", missing, err)
	}
}

// TestCompareEtfsTool 驗證代號正規化與驗證、摘要、查無與錯誤轉換。
func TestCompareEtfsTool(t *testing.T) {
	toolset := func(f *fakeEtfQuerier) *etfToolset {
		return &etfToolset{eq: f, logf: func(string, ...any) {}}
	}
	f := &fakeEtfQuerier{compare: &EtfCompareEnvelope{
		Items: []EtfCompareItem{
			{StockSymbol: "0050", Name: "元大台灣50", Strategy: "market_cap", ManagementFee: "0.32%", TotalAssets: ptr(2.46e12),
				PremiumRate: ptr(0.46), Yield12m: ptr(1.5), DividendFrequency: "semi_annual", Ret1y: ptr(73.95), NavRet1y: ptr(73.92)},
			{StockSymbol: "006208", Name: "富邦台50", Strategy: "market_cap", TotalAssets: ptr(4.5e7)},
		},
		Overlaps: []EtfOverlap{
			{A: "0050", B: "006208", Common: []string{"2330 台積電", "2454 聯發科"}, OverlapWeight: 60.6},
			{A: "0050", B: "00679B", Common: []string{}},
		},
	}}
	result, output, err := toolset(f).compareEtfs(t.Context(), nil, CompareEtfsInput{Symbols: []string{" 0050 ", "006208"}})
	if err != nil || strings.Join(f.gotList, ",") != "0050,006208" {
		t.Fatalf("代號應正規化:%v %v", err, f.gotList)
	}
	if out, ok := output.(CompareEtfsOutput); !ok || out.DataKind != "etf_compare" || len(out.Items) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"比較 2 檔 ETF:",
		"0050 元大台灣50(市值型):經理費 0.32%、保管費 無、規模 24600 億、折溢價 +0.46%(近 20 筆平均 無)、殖利率 1.5%(半年配)、" +
			"近 1 年市價含息 +73.95%／淨值含息 +73.92%",
		"006208 富邦台50(市值型):經理費 無、保管費 無、規模 4500 萬",
		"0050 與 006208 重疊 60.6%(共同 2330 台積電、2454 聯發科)",
		"0050 與 00679B 重疊 0%(沒有共同持股)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	for _, in := range []CompareEtfsInput{
		{Symbols: []string{"0050"}},
		{Symbols: []string{"1", "2", "3", "4", "5"}},
		{Symbols: []string{"0050", "0050"}},
		{Symbols: []string{"0050", ""}},
	} {
		if _, _, err := toolset(f).compareEtfs(t.Context(), nil, in); err == nil {
			t.Errorf("%#v 應回錯誤", in)
		}
	}
	if _, _, err := toolset(&fakeEtfQuerier{}).compareEtfs(t.Context(), nil, CompareEtfsInput{Symbols: []string{"0050", "9999"}}); err == nil ||
		!strings.Contains(err.Error(), "0050、9999 之中有查無的 ETF") {
		t.Fatalf("查無應提示:%v", err)
	}
	if _, _, err := toolset(&fakeEtfQuerier{err: errors.New("boom")}).compareEtfs(t.Context(), nil, CompareEtfsInput{Symbols: []string{"0050", "006208"}}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if summary := compareEtfsSummary(&EtfCompareEnvelope{Items: []EtfCompareItem{{StockSymbol: "0050"}}}); strings.Contains(summary, "前十大持股重疊") {
		t.Fatalf("沒有重疊資料時不應出現該段:%s", summary)
	}
}
