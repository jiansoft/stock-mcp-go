package stock

// 本檔案測試四個 MCP 工具(searchStock、latestDailyQuote、priceHistory、
// stockProfile)的輸入驗證與輸出組裝邏輯,完全不連真實資料庫——這正是
// tools.go 把 Querier 定義成介面帶來的好處(見 tools.go 開頭的說明):
// 這裡注入 fakeQuerier 這個手寫的假實作,測試可以精確控制「資料庫查詢
// 回傳什麼」,專注驗證 tools.go 自己的邏輯(驗證規則、data_as_of 判斷、
// 摘要文字組裝)有沒有寫對,不會被真實資料庫的連線速度或資料變動影響。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeQuerier 是測試用的 Querier 假實作(fake):以測試設定好的固定資料
// 回應查詢,而不是真的查資料庫。
//
// 這是 Go 測試慣用的「手寫 fake」風格,相對於某些語言常見的「用 mock
// 框架自動產生假實作」:因為 Querier 只有四個方法,手寫一個實作型別
// 完全不費工夫,還能避免額外引入 mock 產生工具的學習成本與相依套件。
type fakeQuerier struct {
	// stocks、latest、history、profile:這幾個欄位分別是四個方法各自
	// 要回傳的固定值,測試在建立 fakeQuerier 時依需要填入。
	stocks  []Stock
	latest  *LatestDailyQuote
	history []HistoricalQuote
	profile *StockProfile

	// gotSymbol 記錄「最後一次被呼叫時,傳進來的股票代號實際上是什麼」,
	// 讓測試可以驗證 tools.go 的 normalizeSymbol(trim + 轉大寫)有沒有
	// 真的在呼叫 Querier 之前被套用。
	gotSymbol string
}

// 以下四個方法讓 *fakeQuerier 滿足 Querier 介面(Go 的隱式介面實作:
// 只要方法簽名吻合,不需要任何顯式宣告)。

func (f *fakeQuerier) SearchStock(_ context.Context, query string, limit int) ([]Stock, error) {
	return f.stocks, nil
}

func (f *fakeQuerier) LatestDailyQuote(_ context.Context, symbol string) (*LatestDailyQuote, error) {
	f.gotSymbol = symbol
	return f.latest, nil
}

func (f *fakeQuerier) PriceHistory(_ context.Context, symbol string, from, to *time.Time, limit int) ([]HistoricalQuote, error) {
	f.gotSymbol = symbol
	return f.history, nil
}

func (f *fakeQuerier) StockProfile(_ context.Context, symbol string) (*StockProfile, error) {
	f.gotSymbol = symbol
	return f.profile, nil
}

// fakeSnapshotQuerier 除了滿足 Querier(內嵌 fakeQuerier)之外,還多實作
// RealtimeSnapshot,讓它同時滿足 SnapshotQuerier 介面——用來測試
// AddTools 依型別斷言決定是否註冊 get_realtime_snapshot 工具,以及
// snapshotToolset.realtimeSnapshot 本身的邏輯。
type fakeSnapshotQuerier struct {
	fakeQuerier
	snapshot *RealtimeSnapshot
	err      error

	// gotSymbol 覆蓋(shadow)內嵌 fakeQuerier 的同名欄位,只記錄
	// RealtimeSnapshot 這一個方法實際收到的股票代號。
	gotSymbol string
}

func (f *fakeSnapshotQuerier) RealtimeSnapshot(_ context.Context, symbol string) (*RealtimeSnapshot, error) {
	f.gotSymbol = symbol
	return f.snapshot, f.err
}

// fakeFinancialQuerier 內嵌 fakeQuerier 並多實作三個歷史財務查詢方法,
// 同時滿足 Querier 與 FinancialQuerier——用來測試 AddTools 依型別斷言
// 註冊三個 Phase 1 工具,以及 financialToolset 各 handler 的邏輯。
type fakeFinancialQuerier struct {
	fakeQuerier
	revenues   *MonthlyRevenueHistory
	statements *FinancialStatementHistory
	dividends  *DividendHistory
	err        error

	// gotSymbol / gotRevenueOpt / gotStatementOpt / gotDividendOpt 記錄
	// 最後一次呼叫實際收到的參數,驗證正規化與預設值真的有被套用。
	gotSymbol       string
	gotRevenueOpt   RevenueHistoryOptions
	gotStatementOpt StatementHistoryOptions
	gotDividendOpt  DividendHistoryOptions
}

func (f *fakeFinancialQuerier) MonthlyRevenueHistory(_ context.Context, symbol string, opt RevenueHistoryOptions) (*MonthlyRevenueHistory, error) {
	f.gotSymbol, f.gotRevenueOpt = symbol, opt
	return f.revenues, f.err
}

func (f *fakeFinancialQuerier) FinancialStatementHistory(_ context.Context, symbol string, opt StatementHistoryOptions) (*FinancialStatementHistory, error) {
	f.gotSymbol, f.gotStatementOpt = symbol, opt
	return f.statements, f.err
}

func (f *fakeFinancialQuerier) DividendHistory(_ context.Context, symbol string, opt DividendHistoryOptions) (*DividendHistory, error) {
	f.gotSymbol, f.gotDividendOpt = symbol, opt
	return f.dividends, f.err
}

// newFinancialToolset 用給定的 fake 建立 *financialToolset,logf 同樣
// 傳入空函式。
func newFinancialToolset(f *fakeFinancialQuerier) *financialToolset {
	return &financialToolset{financials: f, logf: func(string, ...any) {}}
}

// newToolset 用給定的 fakeQuerier 建立一個 *toolset,logf 傳入一個
// 「什麼都不做」的空函式——測試不需要檢查 log 內容,只需要確保錯誤
// 記錄不會導致測試本身出錯或印出多餘的雜訊。
func newToolset(f *fakeQuerier) *toolset {
	return &toolset{q: f, logf: func(string, ...any) {}}
}

// testStock 是多個測試共用的一筆固定股票基本資料(台積電),避免每個
// 測試都要重複寫一次相同的 Stock 字面值。
var testStock = Stock{
	StockSymbol:           "2330",
	SecurityCode:          "2330",
	Name:                  "台積電",
	StockExchangeMarketID: 2,
	StockIndustryID:       24,
}

func TestSearchStockTool(t *testing.T) {
	t.Run("query 超過 100 字元回傳驗證錯誤", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{})
		_, _, err := ts.searchStock(t.Context(), nil, SearchStockInput{Query: strings.Repeat("a", 101)})
		if err == nil || !strings.Contains(err.Error(), "query") {
			t.Fatalf("預期 query 長度錯誤,實際為:%v", err)
		}
	})

	t.Run("query 長度以字元數而非 byte 數計算", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{})
		// 「台積電」重複 33 次是 99 個字元(297 byte);
		// 若誤用 byte 數計算,這個合法輸入會被拒絕。
		_, _, err := ts.searchStock(t.Context(), nil, SearchStockInput{Query: strings.Repeat("台積電", 33)})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
	})

	t.Run("limit 超出範圍回傳驗證錯誤", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{})
		_, _, err := ts.searchStock(t.Context(), nil, SearchStockInput{Query: "2330", Limit: 51})
		if err == nil || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("預期 limit 範圍錯誤,實際為:%v", err)
		}
	})

	t.Run("查無資料回傳空陣列而不是錯誤", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{})
		res, out, err := ts.searchStock(t.Context(), nil, SearchStockInput{Query: "不存在"})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		got := out.(SearchStockOutput)
		if got.Stocks == nil || len(got.Stocks) != 0 {
			t.Errorf("預期空陣列,實際為 %#v", got.Stocks)
		}
		if want := "找不到符合關鍵字的股票。"; summaryOf(t, res) != want {
			t.Errorf("摘要應為 %q,實際為 %q", want, summaryOf(t, res))
		}
	})

	t.Run("搜尋成功回傳筆數摘要", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{stocks: []Stock{testStock}})
		res, out, err := ts.searchStock(t.Context(), nil, SearchStockInput{Query: "台積"})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		if got := out.(SearchStockOutput); len(got.Stocks) != 1 {
			t.Errorf("預期 1 筆,實際為 %d 筆", len(got.Stocks))
		}
		if !strings.Contains(summaryOf(t, res), "搜尋到 1 檔股票") {
			t.Errorf("摘要不正確:%q", summaryOf(t, res))
		}
	})
}

func TestLatestDailyQuoteTool(t *testing.T) {
	t.Run("股票不存在回傳工具錯誤", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{latest: nil})
		_, _, err := ts.latestDailyQuote(t.Context(), nil, SymbolInput{Symbol: "9999"})
		if err == nil || !strings.Contains(err.Error(), "找不到股票代號:9999") {
			t.Fatalf("預期找不到股票的錯誤,實際為:%v", err)
		}
	})

	t.Run("代號正規化為 trim 後大寫", func(t *testing.T) {
		f := &fakeQuerier{latest: &LatestDailyQuote{Stock: testStock}}
		ts := newToolset(f)
		_, _, _ = ts.latestDailyQuote(t.Context(), nil, SymbolInput{Symbol: "  00631l "})
		if f.gotSymbol != "00631L" {
			t.Errorf("預期查詢 00631L,實際為 %q", f.gotSymbol)
		}
	})

	t.Run("股票存在但沒有日報價時 quote 為 null", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{latest: &LatestDailyQuote{Stock: testStock, Quote: nil}})
		res, out, err := ts.latestDailyQuote(t.Context(), nil, SymbolInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		got := out.(LatestQuoteOutput)
		if got.Quote != nil {
			t.Error("quote 應為 nil")
		}
		if got.DataAsOf != nil {
			t.Errorf("沒有報價時 data_as_of 應為 nil,實際為 %v", *got.DataAsOf)
		}
		if !strings.Contains(summaryOf(t, res), "沒有最新日報價") {
			t.Errorf("摘要應說明沒有日報價:%q", summaryOf(t, res))
		}
	})

	t.Run("data_as_of 優先使用 updated_time", func(t *testing.T) {
		quote := &DailyQuote{
			Date:        "2026-07-10",
			RecordTime:  ptr("2026-07-10T05:00:00Z"),
			UpdatedTime: ptr("2026-07-10T06:00:00Z"),
		}
		ts := newToolset(&fakeQuerier{latest: &LatestDailyQuote{Stock: testStock, Quote: quote}})
		_, out, err := ts.latestDailyQuote(t.Context(), nil, SymbolInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		got := out.(LatestQuoteOutput)
		if got.DataAsOf == nil || *got.DataAsOf != "2026-07-10T06:00:00Z" {
			t.Errorf("data_as_of 應為 updated_time,實際為 %v", got.DataAsOf)
		}
		if got.DataKind != "latest_daily_quote" || got.IsRealtime {
			t.Errorf("data_kind/is_realtime 欄位不正確:%+v", got)
		}
		if got.Disclaimer != Disclaimer {
			t.Error("必須包含共用免責聲明")
		}
	})
}

func TestPriceHistoryTool(t *testing.T) {
	t.Run("無效日期格式回傳驗證錯誤", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{})
		_, _, err := ts.priceHistory(t.Context(), nil, PriceHistoryInput{Symbol: "2330", From: "2026/07/12"})
		if err == nil || !strings.Contains(err.Error(), "YYYY-MM-DD") {
			t.Fatalf("預期日期格式錯誤,實際為:%v", err)
		}
	})

	t.Run("不存在的日期(2026-13-40)回傳驗證錯誤", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{})
		_, _, err := ts.priceHistory(t.Context(), nil, PriceHistoryInput{Symbol: "2330", From: "2026-13-40"})
		if err == nil {
			t.Fatal("預期日期驗證錯誤,實際成功")
		}
	})

	t.Run("from 晚於 to 回傳驗證錯誤", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{})
		_, _, err := ts.priceHistory(t.Context(), nil, PriceHistoryInput{
			Symbol: "2330", From: "2026-07-12", To: "2026-07-01",
		})
		if err == nil || !strings.Contains(err.Error(), "不可晚於") {
			t.Fatalf("預期 from > to 錯誤,實際為:%v", err)
		}
	})

	t.Run("查無歷史資料回傳空陣列", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{})
		res, out, err := ts.priceHistory(t.Context(), nil, PriceHistoryInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		got := out.(PriceHistoryOutput)
		if got.Quotes == nil || len(got.Quotes) != 0 {
			t.Errorf("預期空陣列,實際為 %#v", got.Quotes)
		}
		if got.DataAsOf != nil {
			t.Error("查無資料時 data_as_of 應為 nil")
		}
		if !strings.Contains(summaryOf(t, res), "未找到股票 2330") {
			t.Errorf("摘要不正確:%q", summaryOf(t, res))
		}
	})

	t.Run("data_as_of 取最新一筆的日期", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{history: []HistoricalQuote{
			{Date: ptr("2026-07-10")},
			{Date: ptr("2026-07-09")},
		}})
		_, out, err := ts.priceHistory(t.Context(), nil, PriceHistoryInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		got := out.(PriceHistoryOutput)
		if got.DataAsOf == nil || *got.DataAsOf != "2026-07-10" {
			t.Errorf("data_as_of 應為 2026-07-10,實際為 %v", got.DataAsOf)
		}
		if got.DataKind != "price_history" {
			t.Errorf("data_kind 不正確:%q", got.DataKind)
		}
	})
}

func TestStockProfileTool(t *testing.T) {
	t.Run("股票不存在回傳工具錯誤", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{profile: nil})
		_, _, err := ts.stockProfile(t.Context(), nil, SymbolInput{Symbol: "9999"})
		if err == nil || !strings.Contains(err.Error(), "找不到股票代號:9999") {
			t.Fatalf("預期找不到股票的錯誤,實際為:%v", err)
		}
	})

	t.Run("缺失的基本面欄位維持 null 並在摘要顯示「無」", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{profile: &StockProfile{Stock: testStock}})
		res, out, err := ts.stockProfile(t.Context(), nil, SymbolInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		got := out.(StockProfileOutput)
		if got.Profile.LastOneEPS != nil || got.Profile.History != nil {
			t.Error("缺失資料必須維持 nil,不可用其他值取代")
		}
		if !strings.Contains(summaryOf(t, res), "近一季 EPS:無") {
			t.Errorf("摘要應以「無」顯示缺失欄位:%q", summaryOf(t, res))
		}
		if got.DataKind != "stock_profile" {
			t.Errorf("data_kind 不正確:%q", got.DataKind)
		}
	})

	t.Run("symbol 為空白字串回傳驗證錯誤", func(t *testing.T) {
		ts := newToolset(&fakeQuerier{})
		_, _, err := ts.stockProfile(t.Context(), nil, SymbolInput{Symbol: "   "})
		if err == nil || !strings.Contains(err.Error(), "symbol") {
			t.Fatalf("預期 symbol 長度錯誤,實際為:%v", err)
		}
	})
}

// newSnapshotToolset 用給定的 fakeSnapshotQuerier 建立一個
// *snapshotToolset,logf 同 newToolset 的理由,傳入空函式。
func newSnapshotToolset(f *fakeSnapshotQuerier) *snapshotToolset {
	return &snapshotToolset{snapshots: f, logf: func(string, ...any) {}}
}

func TestRealtimeSnapshotTool(t *testing.T) {
	t.Run("symbol 為空白字串回傳驗證錯誤", func(t *testing.T) {
		ts := newSnapshotToolset(&fakeSnapshotQuerier{})
		_, _, err := ts.realtimeSnapshot(t.Context(), nil, SymbolInput{Symbol: "   "})
		if err == nil || !strings.Contains(err.Error(), "symbol") {
			t.Fatalf("預期 symbol 長度錯誤,實際為:%v", err)
		}
	})

	t.Run("代號正規化為 trim 後大寫", func(t *testing.T) {
		f := &fakeSnapshotQuerier{snapshot: &RealtimeSnapshot{StockSymbol: "00631L"}}
		ts := newSnapshotToolset(f)
		_, _, _ = ts.realtimeSnapshot(t.Context(), nil, SymbolInput{Symbol: "  00631l "})
		if f.gotSymbol != "00631L" {
			t.Errorf("預期查詢 00631L,實際為 %q", f.gotSymbol)
		}
	})

	t.Run("查無快照時回傳工具錯誤並建議改用日報價", func(t *testing.T) {
		ts := newSnapshotToolset(&fakeSnapshotQuerier{snapshot: nil})
		_, _, err := ts.realtimeSnapshot(t.Context(), nil, SymbolInput{Symbol: "2330"})
		if err == nil || !strings.Contains(err.Error(), "get_latest_daily_quote") {
			t.Fatalf("預期查無快照的錯誤並建議改用 get_latest_daily_quote,實際為:%v", err)
		}
	})

	t.Run("底層查詢錯誤時回傳通用訊息,不外洩內部細節", func(t *testing.T) {
		ts := newSnapshotToolset(&fakeSnapshotQuerier{err: errors.New("dial tcp: 內部主機無法連線")})
		_, _, err := ts.realtimeSnapshot(t.Context(), nil, SymbolInput{Symbol: "2330"})
		if err == nil || err.Error() != errInternal {
			t.Fatalf("預期回傳通用內部錯誤訊息,實際為:%v", err)
		}
	})

	t.Run("查詢成功回傳摘要與 structuredContent", func(t *testing.T) {
		snapshot := &RealtimeSnapshot{
			StockSymbol: "2330",
			Name:        "台積電",
			Price:       ptr(1000.0),
			SourceSite:  "example.com",
			UpdatedAt:   "2026-07-16T05:00:00Z",
		}
		ts := newSnapshotToolset(&fakeSnapshotQuerier{snapshot: snapshot})
		res, out, err := ts.realtimeSnapshot(t.Context(), nil, SymbolInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		if !strings.Contains(summaryOf(t, res), "台積電") || !strings.Contains(summaryOf(t, res), "1000") {
			t.Errorf("摘要應包含股票名稱與價格:%q", summaryOf(t, res))
		}

		// realtimeSnapshot 的 structuredContent 型別是未匯出的匿名 struct,
		// 這裡改用 JSON 往返比對欄位,避免測試耦合到匿名型別的確切定義。
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("json.Marshal(out) 失敗:%v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("json.Unmarshal 失敗:%v", err)
		}
		if got["data_kind"] != "realtime_snapshot" {
			t.Errorf("data_kind 不正確:%v", got["data_kind"])
		}
		if got["is_realtime"] != false {
			t.Errorf("is_realtime 必須為 false,實際為 %v", got["is_realtime"])
		}
		if got["data_as_of"] != "2026-07-16T05:00:00Z" {
			t.Errorf("data_as_of 應等於 snapshot.UpdatedAt,實際為 %v", got["data_as_of"])
		}
		disclaimer, _ := got["disclaimer"].(string)
		if !strings.Contains(disclaimer, "非交易所保證即時行情") {
			t.Errorf("disclaimer 應明確標示非保證即時行情:%q", disclaimer)
		}
		snapOut, ok := got["snapshot"].(map[string]any)
		if !ok {
			t.Fatalf("snapshot 欄位型別不正確:%#v", got["snapshot"])
		}
		if snapOut["stock_symbol"] != "2330" {
			t.Errorf("snapshot.stock_symbol 不正確:%v", snapOut["stock_symbol"])
		}
	})
}

// TestAddToolsRegistersRealtimeSnapshotOnlyWhenSupported 驗證 AddTools 依
// Querier 是否同時滿足 SnapshotQuerier 介面(型別斷言,見 tools.go),
// 決定要不要多註冊 get_realtime_snapshot 這個工具——這是 db 模式(只有
// *Repository)跟 api 模式(*APIClient 額外實作了 RealtimeSnapshot)在
// 工具清單上的唯一差異,必須透過真正的 MCP tools/list 往返驗證,單靠
// 呼叫 snapshotToolset 本身的方法無法測到這段註冊邏輯。
func TestAddToolsRegistersRealtimeSnapshotOnlyWhenSupported(t *testing.T) {
	toolNames := func(t *testing.T, q Querier) []string {
		t.Helper()
		server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
		AddTools(server, q, nil)

		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
		t1, t2 := mcp.NewInMemoryTransports()
		if _, err := server.Connect(t.Context(), t1, nil); err != nil {
			t.Fatalf("server.Connect: %v", err)
		}
		cs, err := client.Connect(t.Context(), t2, nil)
		if err != nil {
			t.Fatalf("client.Connect: %v", err)
		}
		defer cs.Close()

		var names []string
		for tool, err := range cs.Tools(t.Context(), nil) {
			if err != nil {
				t.Fatalf("列出工具失敗:%v", err)
			}
			names = append(names, tool.Name)
		}
		return names
	}

	t.Run("僅實作 Querier 時不註冊 get_realtime_snapshot", func(t *testing.T) {
		names := toolNames(t, &fakeQuerier{})
		if len(names) != 4 {
			t.Errorf("預期 4 個工具,實際為 %d 個:%v", len(names), names)
		}
		for _, name := range names {
			if name == "get_realtime_snapshot" {
				t.Fatalf("不應註冊 get_realtime_snapshot,實際工具清單:%v", names)
			}
		}
	})

	t.Run("同時實作 SnapshotQuerier 時會多註冊 get_realtime_snapshot", func(t *testing.T) {
		names := toolNames(t, &fakeSnapshotQuerier{})
		if len(names) != 5 {
			t.Errorf("預期 5 個工具,實際為 %d 個:%v", len(names), names)
		}
		found := false
		for _, name := range names {
			if name == "get_realtime_snapshot" {
				found = true
			}
		}
		if !found {
			t.Fatalf("預期註冊 get_realtime_snapshot,實際工具清單:%v", names)
		}
	})

	t.Run("同時實作 FinancialQuerier 時會多註冊三個歷史財務工具", func(t *testing.T) {
		names := toolNames(t, &fakeFinancialQuerier{})
		// fakeFinancialQuerier 沒有實作 SnapshotQuerier,因此是 4 + 3 = 7。
		if len(names) != 7 {
			t.Errorf("預期 7 個工具,實際為 %d 個:%v", len(names), names)
		}
		got := map[string]bool{}
		for _, name := range names {
			got[name] = true
		}
		for _, want := range []string{"get_monthly_revenue_history", "get_financial_statement_history", "get_dividend_history"} {
			if !got[want] {
				t.Errorf("預期註冊 %s,實際工具清單:%v", want, names)
			}
		}
	})

	t.Run("僅實作 Querier 時不註冊歷史財務工具", func(t *testing.T) {
		for _, name := range toolNames(t, &fakeQuerier{}) {
			if strings.HasPrefix(name, "get_monthly_revenue") || strings.HasPrefix(name, "get_financial_statement") || strings.HasPrefix(name, "get_dividend") {
				t.Fatalf("db 模式不應註冊 %s", name)
			}
		}
	})
}

// TestMonthlyRevenueHistoryTool 驗證月營收工具的輸入驗證、預設值、摘要
// 與 structuredContent 組裝。
func TestMonthlyRevenueHistoryTool(t *testing.T) {
	// emptyHistory 是「已知股票但查無資料」的 envelope。
	emptyHistory := &MonthlyRevenueHistory{StockSymbol: "2330", Revenues: []MonthlyRevenue{}}

	t.Run("輸入驗證:非法參數在觸及查詢之前被擋下", func(t *testing.T) {
		cases := []struct {
			name    string
			in      MonthlyRevenueInput
			keyword string
		}{
			{"symbol 空白", MonthlyRevenueInput{Symbol: "  "}, "symbol"},
			{"from 格式錯誤", MonthlyRevenueInput{Symbol: "2330", From: "2026/06"}, "from"},
			{"from 月份超界", MonthlyRevenueInput{Symbol: "2330", From: "2026-13"}, "from"},
			{"to 格式錯誤", MonthlyRevenueInput{Symbol: "2330", To: "202606"}, "to"},
			{"from 晚於 to", MonthlyRevenueInput{Symbol: "2330", From: "2026-06", To: "2026-01"}, "不可晚於"},
			{"limit 超界", MonthlyRevenueInput{Symbol: "2330", Limit: 121}, "limit"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				f := &fakeFinancialQuerier{revenues: emptyHistory}
				_, _, err := newFinancialToolset(f).monthlyRevenueHistory(t.Context(), nil, tc.in)
				if err == nil || !strings.Contains(err.Error(), tc.keyword) {
					t.Fatalf("預期含 %q 的驗證錯誤,實際為:%v", tc.keyword, err)
				}
				if f.gotSymbol != "" {
					t.Fatalf("驗證失敗時不應觸及查詢,實際查了 %q", f.gotSymbol)
				}
			})
		}
	})

	t.Run("預設值:未提供 limit 時套用 24,代號正規化為大寫", func(t *testing.T) {
		f := &fakeFinancialQuerier{revenues: emptyHistory}
		_, _, err := newFinancialToolset(f).monthlyRevenueHistory(t.Context(), nil, MonthlyRevenueInput{Symbol: " 2330 "})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		if f.gotSymbol != "2330" || f.gotRevenueOpt.Limit != 24 || f.gotRevenueOpt.From != "" {
			t.Fatalf("參數傳遞錯誤:symbol=%q opt=%+v", f.gotSymbol, f.gotRevenueOpt)
		}
	})

	t.Run("股票不存在時回傳含原始輸入的安全錯誤", func(t *testing.T) {
		f := &fakeFinancialQuerier{err: ErrStockNotFound}
		_, _, err := newFinancialToolset(f).monthlyRevenueHistory(t.Context(), nil, MonthlyRevenueInput{Symbol: "9999"})
		if err == nil || !strings.Contains(err.Error(), "找不到股票代號:9999") {
			t.Fatalf("預期找不到股票的錯誤,實際為:%v", err)
		}
	})

	t.Run("底層錯誤時回傳通用訊息,不外洩內部細節", func(t *testing.T) {
		f := &fakeFinancialQuerier{err: errors.New("dial tcp: 內網主機無法連線")}
		_, _, err := newFinancialToolset(f).monthlyRevenueHistory(t.Context(), nil, MonthlyRevenueInput{Symbol: "2330"})
		if err == nil || err.Error() != errInternal {
			t.Fatalf("預期通用內部錯誤,實際為:%v", err)
		}
	})

	t.Run("查詢成功:摘要含最新月份與年增率,structuredContent 契約完整", func(t *testing.T) {
		f := &fakeFinancialQuerier{revenues: &MonthlyRevenueHistory{
			StockSymbol: "2330",
			DataAsOf:    ptr("2026-06"),
			Revenues: []MonthlyRevenue{{
				Month:               "2026-06",
				MonthlyRevenue:      ptr(263712291.0),
				YearOverYearPercent: ptr(26.9),
			}},
		}}
		res, out, err := newFinancialToolset(f).monthlyRevenueHistory(t.Context(), nil, MonthlyRevenueInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		summary := summaryOf(t, res)
		for _, want := range []string{"2026-06", "26.9", "不構成投資建議"} {
			if !strings.Contains(summary, want) {
				t.Errorf("摘要應包含 %q:%q", want, summary)
			}
		}
		got := roundTripJSON(t, out)
		if got["data_kind"] != "monthly_revenue_history" || got["is_realtime"] != false || got["data_as_of"] != "2026-06" || got["stock_symbol"] != "2330" {
			t.Errorf("structuredContent 共通欄位不正確:%v", got)
		}
		if _, ok := got["revenues"].([]any); !ok {
			t.Errorf("revenues 必須是 JSON 陣列:%#v", got["revenues"])
		}
	})

	t.Run("查無資料:空陣列序列化為 [] 而非 null,data_as_of 為 null", func(t *testing.T) {
		f := &fakeFinancialQuerier{revenues: emptyHistory}
		_, out, err := newFinancialToolset(f).monthlyRevenueHistory(t.Context(), nil, MonthlyRevenueInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("查無資料不是錯誤:%v", err)
		}
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), `"revenues":[]`) {
			t.Errorf("空清單必須序列化為 []:%s", raw)
		}
		if !strings.Contains(string(raw), `"data_as_of":null`) {
			t.Errorf("空清單的 data_as_of 必須為 null:%s", raw)
		}
	})
}

// TestFinancialStatementHistoryTool 驗證財報工具的 period_type 驗證與
// 輸出組裝。
func TestFinancialStatementHistoryTool(t *testing.T) {
	t.Run("period_type 非法值回傳驗證錯誤", func(t *testing.T) {
		f := &fakeFinancialQuerier{}
		_, _, err := newFinancialToolset(f).financialStatementHistory(t.Context(), nil, StatementHistoryInput{Symbol: "2330", PeriodType: "monthly"})
		if err == nil || !strings.Contains(err.Error(), "period_type") {
			t.Fatalf("預期 period_type 驗證錯誤,實際為:%v", err)
		}
	})

	t.Run("未提供 period_type 時預設 quarterly,limit 預設 12", func(t *testing.T) {
		f := &fakeFinancialQuerier{statements: &FinancialStatementHistory{StockSymbol: "2330", Statements: []FinancialStatement{}}}
		_, _, err := newFinancialToolset(f).financialStatementHistory(t.Context(), nil, StatementHistoryInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		if f.gotStatementOpt.PeriodType != "quarterly" || f.gotStatementOpt.Limit != 12 {
			t.Fatalf("預設值錯誤:%+v", f.gotStatementOpt)
		}
	})

	t.Run("查詢成功:摘要含期間標記與 EPS,structuredContent 契約完整", func(t *testing.T) {
		f := &fakeFinancialQuerier{statements: &FinancialStatementHistory{
			StockSymbol: "2330",
			DataAsOf:    ptr("2026-Q1"),
			Statements: []FinancialStatement{{
				Year:             2026,
				Quarter:          "Q1",
				EarningsPerShare: ptr(13.94),
				ReturnOnEquity:   ptr(8.9),
			}},
		}}
		res, out, err := newFinancialToolset(f).financialStatementHistory(t.Context(), nil, StatementHistoryInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		summary := summaryOf(t, res)
		for _, want := range []string{"2026-Q1", "13.94", "不構成投資建議"} {
			if !strings.Contains(summary, want) {
				t.Errorf("摘要應包含 %q:%q", want, summary)
			}
		}
		got := roundTripJSON(t, out)
		if got["data_kind"] != "financial_statement_history" || got["data_as_of"] != "2026-Q1" {
			t.Errorf("structuredContent 不正確:%v", got)
		}
	})
}

// TestDividendHistoryTool 驗證股利工具的年度驗證與輸出組裝。
func TestDividendHistoryTool(t *testing.T) {
	t.Run("年度驗證:超界與順序錯誤都被擋下", func(t *testing.T) {
		cases := []struct {
			name    string
			in      DividendHistoryInput
			keyword string
		}{
			{"from_year 過早", DividendHistoryInput{Symbol: "2330", FromYear: 1889}, "1990"},
			{"to_year 過晚", DividendHistoryInput{Symbol: "2330", ToYear: time.Now().Year() + 2}, "年度"},
			{"from 晚於 to", DividendHistoryInput{Symbol: "2330", FromYear: 2024, ToYear: 2020}, "不可晚於"},
			{"limit 超界", DividendHistoryInput{Symbol: "2330", Limit: 81}, "limit"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				f := &fakeFinancialQuerier{}
				_, _, err := newFinancialToolset(f).dividendHistory(t.Context(), nil, tc.in)
				if err == nil || !strings.Contains(err.Error(), tc.keyword) {
					t.Fatalf("預期含 %q 的驗證錯誤,實際為:%v", tc.keyword, err)
				}
			})
		}
	})

	t.Run("查詢成功:摘要含所屬年度、現金股利與除息日,契約完整", func(t *testing.T) {
		f := &fakeFinancialQuerier{dividends: &DividendHistory{
			StockSymbol: "2330",
			DataAsOf:    ptr("2025-A"),
			Dividends: []Dividend{{
				PaidYear:       2026,
				DividendYear:   2025,
				Quarter:        "A",
				CashDividend:   ptr(17.0),
				StockDividend:  ptr(0.0),
				ExDividendDate: ptr("2026-06-18"),
			}},
		}}
		res, out, err := newFinancialToolset(f).dividendHistory(t.Context(), nil, DividendHistoryInput{Symbol: "2330", FromYear: 2020, ToYear: 2026})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		summary := summaryOf(t, res)
		for _, want := range []string{"2025-A", "17", "2026-06-18", "不構成投資建議"} {
			if !strings.Contains(summary, want) {
				t.Errorf("摘要應包含 %q:%q", want, summary)
			}
		}
		got := roundTripJSON(t, out)
		if got["data_kind"] != "dividend_history" || got["data_as_of"] != "2025-A" {
			t.Errorf("structuredContent 不正確:%v", got)
		}
		if f.gotDividendOpt.FromYear != 2020 || f.gotDividendOpt.ToYear != 2026 || f.gotDividendOpt.Limit != 20 {
			t.Fatalf("參數傳遞錯誤:%+v", f.gotDividendOpt)
		}
	})

	t.Run("null 語意:未公布日期在 JSON 輸出為 null 而非空字串", func(t *testing.T) {
		f := &fakeFinancialQuerier{dividends: &DividendHistory{
			StockSymbol: "2330",
			DataAsOf:    ptr("2025-A"),
			Dividends:   []Dividend{{DividendYear: 2025, Quarter: "A"}},
		}}
		_, out, err := newFinancialToolset(f).dividendHistory(t.Context(), nil, DividendHistoryInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), `"ex_dividend_date":null`) || !strings.Contains(string(raw), `"cash_dividend":null`) {
			t.Errorf("缺值必須是 null:%s", raw)
		}
	})
}

// roundTripJSON 把 structuredContent 輸出經 JSON 序列化再解析成 map,
// 讓測試以「呼叫端實際看到的 JSON 形狀」驗證欄位,而不是耦合 Go 型別。
func roundTripJSON(t *testing.T, out any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal 失敗:%v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal 失敗:%v", err)
	}
	return got
}

// summaryOf 取出工具結果(mcp.CallToolResult)裡第一段文字摘要的內容,
// 方便測試用簡單的字串比對/包含檢查來驗證摘要文字,而不必每次都手動
// 拆解 Content slice 與型別斷言。
func summaryOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("工具結果缺少文字內容")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("第一段內容應為文字,實際為 %T", res.Content[0])
	}
	return tc.Text
}
