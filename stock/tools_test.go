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
	"math"
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
	profile *Profile

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

func (f *fakeQuerier) StockProfile(_ context.Context, symbol string) (*Profile, error) {
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

// fakeAnalyticsQuerier 讓測試精確控制 Phase 2 三個完整 envelope，並記錄
// handler 正規化後真正傳給 client 的 options。
type fakeAnalyticsQuerier struct {
	fakeQuerier
	valuation *ValuationEnvelope
	breadth   *MarketBreadth
	ranking   *DividendYieldRanking
	err       error

	gotSymbol       string
	gotValuationOpt ValuationOptions
	gotBreadthOpt   MarketBreadthOptions
	gotRankingOpt   YieldRankingOptions
}

func (f *fakeAnalyticsQuerier) StockValuation(_ context.Context, symbol string, opt ValuationOptions) (*ValuationEnvelope, error) {
	f.gotSymbol, f.gotValuationOpt = symbol, opt
	return f.valuation, f.err
}

func (f *fakeAnalyticsQuerier) MarketBreadth(_ context.Context, opt MarketBreadthOptions) (*MarketBreadth, error) {
	f.gotBreadthOpt = opt
	return f.breadth, f.err
}

func (f *fakeAnalyticsQuerier) DividendYieldRanking(_ context.Context, opt YieldRankingOptions) (*DividendYieldRanking, error) {
	f.gotRankingOpt = opt
	return f.ranking, f.err
}

// newAnalyticsToolset 建立不輸出測試 log 的 Phase 2 toolset。
func newAnalyticsToolset(f *fakeAnalyticsQuerier) *analyticsToolset {
	return &analyticsToolset{analytics: f, logf: func(string, ...any) {}}
}

// fakeStockScreener 實作 Querier 與 Screener，供 Phase 3 handler 與
// AddTools 能力註冊測試使用。
type fakeStockScreener struct {
	fakeQuerier
	result *Screening
	err    error
	gotOpt ScreenOptions
}

func (f *fakeStockScreener) ScreenStocks(_ context.Context, opt ScreenOptions) (*Screening, error) {
	f.gotOpt = opt
	return f.result, f.err
}

// newScreenToolset 建立不輸出測試 log 的 Phase 3 toolset。
func newScreenToolset(f *fakeStockScreener) *screenToolset {
	return &screenToolset{screener: f, logf: func(string, ...any) {}}
}

// fakeMarketDataQuerier 內嵌 fakeQuerier 並多實作 Phase 4 三個市場輔助
// 查詢方法,同時滿足 Querier 與 MarketDataQuerier——用來測試 AddTools
// 依型別斷言註冊三個工具,以及 marketDataToolset 各 handler 的邏輯。
type fakeMarketDataQuerier struct {
	fakeQuerier
	index    *MarketIndexHistory
	calendar *DividendCalendar
	qfii     *QfiiHoldingRanking
	movers   *MarketMovers
	err      error

	// gotIndexOpt / gotCalendarOpt / gotQfiiOpt / gotMoversOpt 記錄最後
	// 一次呼叫實際收到的參數,驗證正規化與預設值真的有被套用。
	gotIndexOpt    IndexHistoryOptions
	gotCalendarOpt CalendarOptions
	gotQfiiOpt     QfiiRankingOptions
	gotMoversOpt   MoversOptions
}

func (f *fakeMarketDataQuerier) MarketIndexHistory(_ context.Context, opt IndexHistoryOptions) (*MarketIndexHistory, error) {
	f.gotIndexOpt = opt
	return f.index, f.err
}

func (f *fakeMarketDataQuerier) DividendCalendar(_ context.Context, opt CalendarOptions) (*DividendCalendar, error) {
	f.gotCalendarOpt = opt
	return f.calendar, f.err
}

func (f *fakeMarketDataQuerier) QfiiHoldingRanking(_ context.Context, opt QfiiRankingOptions) (*QfiiHoldingRanking, error) {
	f.gotQfiiOpt = opt
	return f.qfii, f.err
}

func (f *fakeMarketDataQuerier) MarketMovers(_ context.Context, opt MoversOptions) (*MarketMovers, error) {
	f.gotMoversOpt = opt
	return f.movers, f.err
}

// newMarketDataToolset 建立不輸出測試 log 的 Phase 4 toolset。
func newMarketDataToolset(f *fakeMarketDataQuerier) *marketDataToolset {
	return &marketDataToolset{marketData: f, logf: func(string, ...any) {}}
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
		ts := newToolset(&fakeQuerier{profile: &Profile{Stock: testStock}})
		res, out, err := ts.stockProfile(t.Context(), nil, SymbolInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		got := out.(ProfileOutput)
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

	t.Run("同時實作 AnalyticsQuerier 時只新增三個 Phase 2 工具", func(t *testing.T) {
		names := toolNames(t, &fakeAnalyticsQuerier{})
		if len(names) != 7 {
			t.Fatalf("預期 4+3 個工具,實際為 %d:%v", len(names), names)
		}
		got := map[string]bool{}
		for _, name := range names {
			got[name] = true
		}
		for _, want := range []string{"get_stock_valuation", "get_market_breadth", "get_dividend_yield_ranking"} {
			if !got[want] {
				t.Errorf("預期註冊 %s,實際工具清單:%v", want, names)
			}
		}
		if got["get_monthly_revenue_history"] {
			t.Fatal("僅實作 AnalyticsQuerier 時不應連帶註冊 Phase 1 工具")
		}
	})

	t.Run("同時實作 MarketDataQuerier 時只新增四個市場工具", func(t *testing.T) {
		names := toolNames(t, &fakeMarketDataQuerier{})
		// fakeMarketDataQuerier 只多實作 MarketDataQuerier,因此是
		// 4 + 4 = 8(Phase 4 的三個工具,加上後續新增的 get_market_movers)。
		if len(names) != 8 {
			t.Fatalf("預期 4+4 個工具,實際為 %d:%v", len(names), names)
		}
		got := map[string]bool{}
		for _, name := range names {
			got[name] = true
		}
		for _, want := range []string{"get_market_index_history", "get_dividend_calendar", "get_qfii_holding_ranking", "get_market_movers"} {
			if !got[want] {
				t.Errorf("預期註冊 %s,實際工具清單:%v", want, names)
			}
		}
		if got["get_monthly_revenue_history"] || got["get_stock_valuation"] || got["screen_stocks"] {
			t.Fatal("僅實作 MarketDataQuerier 時不應連帶註冊其他 Phase 的工具")
		}
	})

	t.Run("僅實作 Querier 時不註冊市場輔助工具", func(t *testing.T) {
		for _, name := range toolNames(t, &fakeQuerier{}) {
			if name == "get_market_index_history" || name == "get_dividend_calendar" || name == "get_qfii_holding_ranking" {
				t.Fatalf("db 模式不應註冊 %s", name)
			}
		}
	})

	t.Run("StockScreener 能力只新增 screen_stocks", func(t *testing.T) {
		names := toolNames(t, &fakeStockScreener{})
		if len(names) != 5 {
			t.Fatalf("預期 4+1 個工具,實際為 %d:%v", len(names), names)
		}
		found := false
		for _, name := range names {
			if name == "screen_stocks" {
				found = true
			}
			if name == "get_stock_valuation" {
				t.Fatal("只實作 StockScreener 不應連帶註冊 Phase 2")
			}
		}
		if !found {
			t.Fatalf("tools/list 缺少 screen_stocks:%v", names)
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

// TestAnalyticsTools 驗證 Phase 2 的輸入正規化、預設值、安全錯誤、繁中
// 摘要，以及 Data API fixture 到 structuredContent 的數值/null/[] 一致性。
func TestAnalyticsTools(t *testing.T) {
	t.Run("估值:代號日期正規化且完整 envelope 原樣輸出", func(t *testing.T) {
		fixture := &ValuationEnvelope{
			StockSymbol: "2330", DataAsOf: ptr("2026-07-16"),
			Valuation: &Valuation{StockSymbol: "2330", Date: "2026-07-16", ClosingPrice: ptr(1045.0), Percentage: ptr(50.0), YearCount: 5, Cheap: ptr(800.0), Fair: ptr(1000.0), Expensive: ptr(1200.0), ValuationBand: "overvalued"},
		}
		f := &fakeAnalyticsQuerier{valuation: fixture}
		res, out, err := newAnalyticsToolset(f).stockValuation(t.Context(), nil, ValuationInput{Symbol: " 2330 ", Date: "2026-07-16"})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		if f.gotSymbol != "2330" || f.gotValuationOpt.Date != "2026-07-16" {
			t.Fatalf("正規化參數錯誤:%q %+v", f.gotSymbol, f.gotValuationOpt)
		}
		summary := summaryOf(t, res)
		for _, want := range []string{"2026-07-16", "1045", "overvalued", "不是目標價", "不構成投資建議"} {
			if !strings.Contains(summary, want) {
				t.Errorf("摘要缺少 %q:%q", want, summary)
			}
		}
		got := roundTripJSON(t, out)
		if got["data_kind"] != "stock_valuation" || got["data_as_of"] != "2026-07-16" || got["is_realtime"] != false || got["disclaimer"] != AnalysisDisclaimer {
			t.Errorf("估值共通欄位錯誤:%v", got)
		}
		valuation := got["valuation"].(map[string]any)
		if valuation["closing_price"] != 1045.0 || valuation["percentage"] != 50.0 || valuation["valuation_band"] != "overvalued" {
			t.Errorf("估值 fixture 未原樣保留:%v", valuation)
		}
	})

	t.Run("估值:無資料保留 null且不存在股票/內部錯誤分層安全", func(t *testing.T) {
		f := &fakeAnalyticsQuerier{valuation: &ValuationEnvelope{StockSymbol: "4414"}}
		_, out, err := newAnalyticsToolset(f).stockValuation(t.Context(), nil, ValuationInput{Symbol: "4414"})
		if err != nil {
			t.Fatalf("查無估值不是錯誤:%v", err)
		}
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), `"data_as_of":null`) || !strings.Contains(string(raw), `"valuation":null`) {
			t.Fatalf("null 語意錯誤:%s", raw)
		}

		f.err = ErrStockNotFound
		_, _, err = newAnalyticsToolset(f).stockValuation(t.Context(), nil, ValuationInput{Symbol: "9999"})
		if err == nil || !strings.Contains(err.Error(), "找不到股票代號:9999") {
			t.Fatalf("404 應回可理解的 tool error:%v", err)
		}
		f.err = errors.New("dial tcp 10.0.0.1:5432: secret")
		_, _, err = newAnalyticsToolset(f).stockValuation(t.Context(), nil, ValuationInput{Symbol: "2330"})
		if err == nil || err.Error() != errInternal || strings.Contains(err.Error(), "10.0.0.1") {
			t.Fatalf("內部錯誤必須安全:%v", err)
		}
	})

	t.Run("市場廣度:預設值、序列與 breadth 等於 history 首筆", func(t *testing.T) {
		point := MarketBreadthPoint{Date: "2026-07-16", Market: "all", Undervalued: 100, FairValued: 200, Overvalued: 300, HighlyOvervalued: 50, StocksUp: 700, StocksDown: 400, StocksUnchanged: 100}
		f := &fakeAnalyticsQuerier{breadth: &MarketBreadth{DataAsOf: "2026-07-16", Breadth: point, History: []MarketBreadthPoint{point}}}
		res, out, err := newAnalyticsToolset(f).marketBreadth(t.Context(), nil, MarketBreadthInput{})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		if f.gotBreadthOpt.Market != "all" || f.gotBreadthOpt.Days != 1 {
			t.Fatalf("預設值錯誤:%+v", f.gotBreadthOpt)
		}
		for _, want := range []string{"2026-07-16", "上漲 700", "低估 100", "不構成投資建議"} {
			if !strings.Contains(summaryOf(t, res), want) {
				t.Errorf("摘要缺少 %q:%q", want, summaryOf(t, res))
			}
		}
		got := roundTripJSON(t, out)
		if got["data_kind"] != "market_breadth" || got["data_as_of"] != "2026-07-16" || got["is_realtime"] != false {
			t.Errorf("廣度共通欄位錯誤:%v", got)
		}
		breadth := got["breadth"].(map[string]any)
		history := got["history"].([]any)[0].(map[string]any)
		if breadth["stocks_up"] != history["stocks_up"] || breadth["stocks_up"] != 700.0 {
			t.Errorf("breadth/history fixture 不一致:%v %v", breadth, history)
		}
	})

	t.Run("殖利率排行:預設值、空陣列與 fixture 數值", func(t *testing.T) {
		rank := YieldRank{Rank: 1, StockSymbol: "1234", Name: "測試公司", MarketID: 2, IndustryID: 24, Date: "2026-07-16", ClosingPrice: ptr(50.0), Dividend: ptr(5.0), DividendYieldPercent: ptr(10.0)}
		f := &fakeAnalyticsQuerier{ranking: &DividendYieldRanking{DataAsOf: "2026-07-16", Stocks: []YieldRank{rank}}}
		res, out, err := newAnalyticsToolset(f).dividendYieldRanking(t.Context(), nil, YieldRankingInput{IndustryID: 24})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		if f.gotRankingOpt.Market != "all" || f.gotRankingOpt.Limit != 20 || f.gotRankingOpt.IndustryID != 24 {
			t.Fatalf("排行參數錯誤:%+v", f.gotRankingOpt)
		}
		for _, want := range []string{"1234", "測試公司", "10%", "不構成投資建議"} {
			if !strings.Contains(summaryOf(t, res), want) {
				t.Errorf("摘要缺少 %q:%q", want, summaryOf(t, res))
			}
		}
		got := roundTripJSON(t, out)
		stocks := got["stocks"].([]any)
		if got["data_kind"] != "dividend_yield_ranking" || stocks[0].(map[string]any)["dividend_yield_percent"] != 10.0 {
			t.Errorf("排行 structuredContent 錯誤:%v", got)
		}

		f.ranking = &DividendYieldRanking{DataAsOf: "2026-07-16", Stocks: []YieldRank{}}
		_, emptyOut, err := newAnalyticsToolset(f).dividendYieldRanking(t.Context(), nil, YieldRankingInput{})
		if err != nil {
			t.Fatalf("空排行不是錯誤:%v", err)
		}
		raw, _ := json.Marshal(emptyOut)
		if !strings.Contains(string(raw), `"stocks":[]`) || !strings.Contains(string(raw), `"data_as_of":"2026-07-16"`) {
			t.Errorf("空排行必須保留 [] 與伺服器選定的資料日:%s", raw)
		}
	})

	t.Run("輸入上下界與 enum 在查詢前被擋下", func(t *testing.T) {
		cases := []struct {
			name string
			call func() error
		}{
			{"估值日期", func() error {
				_, _, err := newAnalyticsToolset(&fakeAnalyticsQuerier{}).stockValuation(t.Context(), nil, ValuationInput{Symbol: "2330", Date: "2026-13-40"})
				return err
			}},
			{"廣度市場", func() error {
				_, _, err := newAnalyticsToolset(&fakeAnalyticsQuerier{}).marketBreadth(t.Context(), nil, MarketBreadthInput{Market: "otc"})
				return err
			}},
			{"廣度 days", func() error {
				_, _, err := newAnalyticsToolset(&fakeAnalyticsQuerier{}).marketBreadth(t.Context(), nil, MarketBreadthInput{Days: 61})
				return err
			}},
			{"排行產業", func() error {
				_, _, err := newAnalyticsToolset(&fakeAnalyticsQuerier{}).dividendYieldRanking(t.Context(), nil, YieldRankingInput{IndustryID: -1})
				return err
			}},
			{"排行 limit", func() error {
				_, _, err := newAnalyticsToolset(&fakeAnalyticsQuerier{}).dividendYieldRanking(t.Context(), nil, YieldRankingInput{Limit: 51})
				return err
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := tc.call(); err == nil {
					t.Fatal("預期驗證錯誤,實際成功")
				}
			})
		}
	})

	t.Run("市場資料不存在時回可理解的 tool error", func(t *testing.T) {
		f := &fakeAnalyticsQuerier{err: ErrMarketDataNotFound}
		_, _, err := newAnalyticsToolset(f).marketBreadth(t.Context(), nil, MarketBreadthInput{})
		if err == nil || !strings.Contains(err.Error(), "查無市場廣度資料") || err.Error() == errInternal {
			t.Fatalf("廣度 404 應是可理解的資料錯誤:%v", err)
		}
		_, _, err = newAnalyticsToolset(f).dividendYieldRanking(t.Context(), nil, YieldRankingInput{})
		if err == nil || !strings.Contains(err.Error(), "查無殖利率排行資料") || err.Error() == errInternal {
			t.Fatalf("排行 404 應是可理解的資料錯誤:%v", err)
		}
	})
}

// TestAnalyticsToolsAreReadOnly 經由真正 tools/list 驗證 Phase 2 每個工具
// 都帶 ReadOnlyHint，避免只測 handler 而漏掉註冊 metadata。
func TestAnalyticsToolsAreReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeAnalyticsQuerier{}, nil)
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
	wants := map[string]bool{"get_stock_valuation": false, "get_market_breadth": false, "get_dividend_yield_ranking": false}
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if _, ok := wants[tool.Name]; !ok {
			continue
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s 必須有 ReadOnlyHint=true", tool.Name)
		}
		wants[tool.Name] = true
	}
	for name, found := range wants {
		if !found {
			t.Errorf("tools/list 缺少 %s", name)
		}
	}
}

// TestScreenStocksTool 驗證 Phase 3 的實質 filter、固定排序白名單、預設值、
// structuredContent、null/[] 與安全錯誤分層。
func TestScreenStocksTool(t *testing.T) {
	empty := &Screening{Stocks: []ScreenedStock{}}

	t.Run("market all 單獨或只有排序不算實質篩選", func(t *testing.T) {
		for _, in := range []ScreenStocksInput{{}, {Market: "all"}, {Market: "all", SortBy: "eps", SortOrder: "desc", Limit: 10}} {
			_, _, err := newScreenToolset(&fakeStockScreener{result: empty}).screenStocks(t.Context(), nil, in)
			if err == nil || !strings.Contains(err.Error(), "實質篩選條件") {
				t.Fatalf("應拒絕無篩選查詢,輸入 %+v,錯誤 %v", in, err)
			}
		}
	})

	t.Run("市場產業估值或任一 min 都可作實質篩選", func(t *testing.T) {
		zero := 0.0
		cases := []ScreenStocksInput{
			{Market: "twse"}, {Market: "tpex"}, {IndustryID: 24}, {ValuationBand: "undervalued"},
			{MinRevenueYOYPercent: &zero}, {MinEPS: &zero}, {MinROEPercent: &zero}, {MinDividendYieldPercent: &zero},
		}
		for _, in := range cases {
			f := &fakeStockScreener{result: empty}
			_, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, in)
			if err != nil {
				t.Errorf("合法實質篩選 %+v 不應失敗:%v", in, err)
				continue
			}
			if f.gotOpt.SortBy != "stock_symbol" || f.gotOpt.SortOrder != "asc" || f.gotOpt.Limit != 20 {
				t.Errorf("預設排序/筆數錯誤:%+v", f.gotOpt)
			}
		}
	})

	t.Run("六個 sort 欄位與兩方向固定白名單", func(t *testing.T) {
		for _, sortBy := range []string{"stock_symbol", "revenue_yoy", "eps", "roe", "dividend_yield", "valuation_percentage"} {
			for _, order := range []string{"asc", "desc"} {
				f := &fakeStockScreener{result: empty}
				_, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{Market: "twse", SortBy: sortBy, SortOrder: order})
				if err != nil || f.gotOpt.SortBy != sortBy || f.gotOpt.SortOrder != order {
					t.Errorf("sort %s/%s 傳遞錯誤:%+v, %v", sortBy, order, f.gotOpt, err)
				}
			}
		}
	})

	t.Run("非法 enum 與數值上下界在呼叫 client 前被擋下", func(t *testing.T) {
		cases := []struct {
			name string
			in   ScreenStocksInput
		}{
			{"market", ScreenStocksInput{Market: "otc", IndustryID: 24}},
			{"industry", ScreenStocksInput{IndustryID: -1}},
			{"valuation", ScreenStocksInput{ValuationBand: "cheap"}},
			{"revenue low", ScreenStocksInput{MinRevenueYOYPercent: ptr(-100.1)}},
			{"revenue high", ScreenStocksInput{MinRevenueYOYPercent: ptr(10000.1)}},
			{"eps", ScreenStocksInput{MinEPS: ptr(10000.1)}},
			{"roe", ScreenStocksInput{MinROEPercent: ptr(-10000.1)}},
			{"yield", ScreenStocksInput{MinDividendYieldPercent: ptr(-0.1)}},
			{"revenue NaN", ScreenStocksInput{MinRevenueYOYPercent: ptr(math.NaN())}},
			{"eps positive infinity", ScreenStocksInput{MinEPS: ptr(math.Inf(1))}},
			{"roe negative infinity", ScreenStocksInput{MinROEPercent: ptr(math.Inf(-1))}},
			{"sort by", ScreenStocksInput{Market: "twse", SortBy: "name"}},
			{"sort order", ScreenStocksInput{Market: "twse", SortOrder: "sideways"}},
			{"limit", ScreenStocksInput{Market: "twse", Limit: 51}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				f := &fakeStockScreener{result: empty}
				_, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, tc.in)
				if err == nil {
					t.Fatal("預期驗證錯誤,實際成功")
				}
				if f.gotOpt.Limit != 0 {
					t.Fatal("驗證失敗不可觸及 client")
				}
			})
		}
	})

	t.Run("breadth 與排行選股使用不同 market 說明", func(t *testing.T) {
		breadth := breadthMarketSchema().Description
		listedOTC := listedOTCMarketSchema().Description
		if !strings.Contains(breadth, "id 0") || strings.Contains(breadth, "不含公開發行") {
			t.Fatalf("breadth all 必須描述統計表 id 0 合併列:%q", breadth)
		}
		if !strings.Contains(listedOTC, "不含公開發行與興櫃") || breadth == listedOTC {
			t.Fatalf("排行/選股 all 必須描述上市加上櫃邊界:%q", listedOTC)
		}
	})

	t.Run("完整 fixture 原樣進入 structuredContent 且摘要揭露各指標日期", func(t *testing.T) {
		fixture := &Screening{Stocks: []ScreenedStock{{
			StockSymbol: "2330", Name: "台積電", MarketID: 2, IndustryID: 24,
			RevenueYOYPercent: ptr(26.9), EarningsPerShare: ptr(13.94), ReturnOnEquity: ptr(8.9),
			DividendYieldPercent: ptr(2.1), ValuationBand: ptr("fair_valued"), ValuationPercentage: ptr(130.6),
			RevenueMonth: ptr("2026-06"), FinancialPeriod: ptr("2026-Q1"), ValuationDate: ptr("2026-07-16"), YieldDate: nil,
		}}}
		f := &fakeStockScreener{result: fixture}
		res, out, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{Market: "twse", IndustryID: 24, MinEPS: ptr(5.0), SortBy: "dividend_yield", SortOrder: "desc"})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		for _, want := range []string{"2330", "26.9", "13.94", "2026-06", "2026-Q1", "2026-07-16", "殖利率日 無", "不構成買賣建議", "不構成投資建議"} {
			if !strings.Contains(summaryOf(t, res), want) {
				t.Errorf("摘要缺少 %q:%q", want, summaryOf(t, res))
			}
		}
		got := roundTripJSON(t, out)
		if got["data_kind"] != "stock_screening_result" || got["data_as_of"] != nil || got["is_realtime"] != false || got["disclaimer"] != AnalysisDisclaimer {
			t.Errorf("screen 共通欄位錯誤:%v", got)
		}
		stock := got["stocks"].([]any)[0].(map[string]any)
		if stock["revenue_yoy_percent"] != 26.9 || stock["earnings_per_share"] != 13.94 || stock["yield_date"] != nil {
			t.Errorf("screen fixture/null 未原樣保留:%v", stock)
		}
		if f.gotOpt.Market != "twse" || f.gotOpt.IndustryID != 24 || f.gotOpt.MinEPS == nil || *f.gotOpt.MinEPS != 5 || f.gotOpt.Limit != 20 {
			t.Errorf("client options 傳遞錯誤:%+v", f.gotOpt)
		}
	})

	t.Run("空結果為 [] 且內部錯誤只回安全通用訊息", func(t *testing.T) {
		f := &fakeStockScreener{result: empty}
		_, out, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{Market: "twse"})
		if err != nil {
			t.Fatalf("空結果不是錯誤:%v", err)
		}
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), `"stocks":[]`) || !strings.Contains(string(raw), `"data_as_of":null`) {
			t.Errorf("空結果必須保留 []/null:%s", raw)
		}

		f.err = errors.New("dial tcp 10.0.0.1:5432: secret")
		_, _, err = newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{Market: "twse"})
		if err == nil || err.Error() != errInternal || strings.Contains(err.Error(), "10.0.0.1") {
			t.Fatalf("內部錯誤必須安全:%v", err)
		}
	})
}

// TestScreenStocksIsReadOnly 經 tools/list 驗證能力註冊與 ReadOnlyHint。
func TestScreenStocksIsReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeStockScreener{}, nil)
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
		if tool.Name == "screen_stocks" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("screen_stocks 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Fatal("tools/list 缺少 screen_stocks")
	}
}

// TestMarketDataTools 驗證 Phase 4 三個市場輔助工具的輸入驗證、預設值、
// 繁中摘要、structuredContent 契約、空陣列/null 語意與安全錯誤分層。
func TestMarketDataTools(t *testing.T) {
	// 三個「已知條件合法但查無資料」的空 envelope,對應市場層級 endpoint
	// 的 200 空陣列語意。
	emptyIndex := &MarketIndexHistory{Points: []IndexPoint{}}
	emptyCalendar := &DividendCalendar{Events: []DividendEvent{}}
	emptyQfii := &QfiiHoldingRanking{Stocks: []QfiiHolding{}}

	t.Run("輸入驗證:非法參數在觸及查詢之前被擋下", func(t *testing.T) {
		cases := []struct {
			name    string
			call    func(f *fakeMarketDataQuerier) error
			keyword string
		}{
			{"指數 from 格式錯誤", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).marketIndexHistory(t.Context(), nil, IndexHistoryInput{From: "2026/06/01"})
				return err
			}, "from"},
			{"指數 from 日期不存在", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).marketIndexHistory(t.Context(), nil, IndexHistoryInput{From: "2026-13-40"})
				return err
			}, "from"},
			{"指數 from 晚於 to", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).marketIndexHistory(t.Context(), nil, IndexHistoryInput{From: "2026-07-17", To: "2026-06-01"})
				return err
			}, "不可晚於"},
			{"指數 limit 超界", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).marketIndexHistory(t.Context(), nil, IndexHistoryInput{Limit: 366})
				return err
			}, "limit"},
			{"行事曆 to 格式錯誤", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).dividendCalendar(t.Context(), nil, DividendCalendarInput{To: "20260731"})
				return err
			}, "to"},
			{"行事曆 from 晚於 to", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).dividendCalendar(t.Context(), nil, DividendCalendarInput{From: "2026-07-31", To: "2026-07-01"})
				return err
			}, "不可晚於"},
			{"行事曆區間超過 92 天", func(f *fakeMarketDataQuerier) error {
				// 2026-07-01 到 2026-10-02 是 93 天,必須被擋下。
				_, _, err := newMarketDataToolset(f).dividendCalendar(t.Context(), nil, DividendCalendarInput{From: "2026-07-01", To: "2026-10-02"})
				return err
			}, "92 天"},
			{"行事曆 event_type 非法", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).dividendCalendar(t.Context(), nil, DividendCalendarInput{EventType: "dividend"})
				return err
			}, "event_type"},
			{"行事曆 limit 超界", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).dividendCalendar(t.Context(), nil, DividendCalendarInput{Limit: 201})
				return err
			}, "limit"},
			{"QFII market 非法", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).qfiiHoldingRanking(t.Context(), nil, QfiiRankingInput{Market: "otc"})
				return err
			}, "market"},
			{"QFII industry_id 為負", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).qfiiHoldingRanking(t.Context(), nil, QfiiRankingInput{IndustryID: -1})
				return err
			}, "industry_id"},
			{"QFII sort_by 非法", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).qfiiHoldingRanking(t.Context(), nil, QfiiRankingInput{SortBy: "amount"})
				return err
			}, "sort_by"},
			{"QFII limit 超界", func(f *fakeMarketDataQuerier) error {
				_, _, err := newMarketDataToolset(f).qfiiHoldingRanking(t.Context(), nil, QfiiRankingInput{Limit: 51})
				return err
			}, "limit"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				f := &fakeMarketDataQuerier{index: emptyIndex, calendar: emptyCalendar, qfii: emptyQfii}
				err := tc.call(f)
				if err == nil || !strings.Contains(err.Error(), tc.keyword) {
					t.Fatalf("預期含 %q 的驗證錯誤,實際為:%v", tc.keyword, err)
				}
				// 驗證失敗時不可觸及查詢(options 應維持零值)。
				if f.gotIndexOpt.Limit != 0 || f.gotCalendarOpt.Limit != 0 || f.gotQfiiOpt.Limit != 0 {
					t.Fatal("驗證失敗時不應觸及查詢")
				}
			})
		}
	})

	t.Run("預設值:三個工具未提供選填參數時套用契約預設", func(t *testing.T) {
		f := &fakeMarketDataQuerier{index: emptyIndex, calendar: emptyCalendar, qfii: emptyQfii}
		mts := newMarketDataToolset(f)
		if _, _, err := mts.marketIndexHistory(t.Context(), nil, IndexHistoryInput{}); err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		if f.gotIndexOpt.Limit != 30 || f.gotIndexOpt.From != "" || f.gotIndexOpt.To != "" {
			t.Fatalf("指數預設值錯誤:%+v", f.gotIndexOpt)
		}
		if _, _, err := mts.dividendCalendar(t.Context(), nil, DividendCalendarInput{}); err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		if f.gotCalendarOpt.EventType != "all" || f.gotCalendarOpt.Limit != 50 || f.gotCalendarOpt.From != "" || f.gotCalendarOpt.To != "" {
			t.Fatalf("行事曆預設值錯誤:%+v", f.gotCalendarOpt)
		}
		if _, _, err := mts.qfiiHoldingRanking(t.Context(), nil, QfiiRankingInput{}); err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		if f.gotQfiiOpt.Market != "all" || f.gotQfiiOpt.SortBy != "percentage" || f.gotQfiiOpt.Limit != 20 || f.gotQfiiOpt.IndustryID != 0 {
			t.Fatalf("QFII 預設值錯誤:%+v", f.gotQfiiOpt)
		}
	})

	t.Run("行事曆:恰好 92 天的區間是合法的邊界值", func(t *testing.T) {
		f := &fakeMarketDataQuerier{calendar: emptyCalendar}
		// 2026-07-01 到 2026-10-01 恰為 92 天,契約允許(不可「超過」92 天)。
		_, _, err := newMarketDataToolset(f).dividendCalendar(t.Context(), nil, DividendCalendarInput{From: "2026-07-01", To: "2026-10-01"})
		if err != nil {
			t.Fatalf("92 天邊界應合法:%v", err)
		}
		if f.gotCalendarOpt.From != "2026-07-01" || f.gotCalendarOpt.To != "2026-10-01" {
			t.Fatalf("日期傳遞錯誤:%+v", f.gotCalendarOpt)
		}
	})

	t.Run("指數歷史:摘要含最新點位,structuredContent 契約完整", func(t *testing.T) {
		f := &fakeMarketDataQuerier{index: &MarketIndexHistory{
			DataAsOf: ptr("2026-07-17"),
			Points: []IndexPoint{
				{Date: "2026-07-17", Index: ptr(23000.5), Change: ptr(120.3), TradeValue: ptr(412345678901.0)},
				{Date: "2026-07-16", Index: ptr(22880.2), Change: ptr(-50.1)},
			},
		}}
		res, out, err := newMarketDataToolset(f).marketIndexHistory(t.Context(), nil, IndexHistoryInput{From: "2026-06-01", To: "2026-07-17"})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		summary := summaryOf(t, res)
		for _, want := range []string{"TAIEX", "2026-07-17", "23000.5", "120.3", "不構成投資建議"} {
			if !strings.Contains(summary, want) {
				t.Errorf("摘要應包含 %q:%q", want, summary)
			}
		}
		got := roundTripJSON(t, out)
		if got["data_kind"] != "market_index_history" || got["is_realtime"] != false || got["data_as_of"] != "2026-07-17" || got["disclaimer"] != AnalysisDisclaimer {
			t.Errorf("structuredContent 共通欄位不正確:%v", got)
		}
		points, ok := got["points"].([]any)
		if !ok || len(points) != 2 {
			t.Fatalf("points 必須是 JSON 陣列:%#v", got["points"])
		}
		latest := points[0].(map[string]any)
		if latest["index"] != 23000.5 || latest["transaction"] != nil {
			t.Errorf("指數 fixture/null 未原樣保留:%v", latest)
		}
	})

	t.Run("行事曆:摘要含最近事件與中文事件名稱,data_as_of 為 null", func(t *testing.T) {
		f := &fakeMarketDataQuerier{calendar: &DividendCalendar{
			Events: []DividendEvent{{
				StockSymbol: "2330", Name: "台積電", EventType: "ex_dividend", EventDate: "2026-07-20",
				DividendYear: 2025, Quarter: "A", CashDividend: ptr(17.0), StockDividend: ptr(0.0), TotalDividend: ptr(17.0),
			}},
		}}
		res, out, err := newMarketDataToolset(f).dividendCalendar(t.Context(), nil, DividendCalendarInput{From: "2026-07-01", To: "2026-07-31"})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		summary := summaryOf(t, res)
		for _, want := range []string{"2026-07-20", "2330", "台積電", "除息", "17", "不構成投資建議"} {
			if !strings.Contains(summary, want) {
				t.Errorf("摘要應包含 %q:%q", want, summary)
			}
		}
		got := roundTripJSON(t, out)
		if got["data_kind"] != "dividend_calendar" || got["is_realtime"] != false || got["data_as_of"] != nil {
			t.Errorf("structuredContent 共通欄位不正確:%v", got)
		}
		events := got["events"].([]any)
		event := events[0].(map[string]any)
		// structuredContent 內維持英文事件代碼,不因摘要翻譯而改變契約欄位。
		if event["event_type"] != "ex_dividend" || event["cash_dividend"] != 17.0 || event["stock_dividend"] != 0.0 {
			t.Errorf("行事曆 fixture 未原樣保留:%v", event)
		}
	})

	t.Run("QFII:摘要標明快照語意,data_as_of 為 null 且股數保留精度", func(t *testing.T) {
		f := &fakeMarketDataQuerier{qfii: &QfiiHoldingRanking{
			Stocks: []QfiiHolding{{
				Rank: 1, StockSymbol: "2330", Name: "台積電", MarketID: 2, IndustryID: 24,
				QfiiSharesHeld: ptr(int64(19000000000)), QfiiShareHoldingPercentage: ptr(73.2), IssuedShare: ptr(int64(25930000000)),
			}},
		}}
		res, out, err := newMarketDataToolset(f).qfiiHoldingRanking(t.Context(), nil, QfiiRankingInput{Market: "twse", IndustryID: 24})
		if err != nil {
			t.Fatalf("不應失敗:%v", err)
		}
		summary := summaryOf(t, res)
		// §4.10 硬性要求:摘要必須標示快照、無歷史序列。
		for _, want := range []string{"快照", "無歷史序列", "2330", "台積電", "73.2", "19000000000", "不構成投資建議"} {
			if !strings.Contains(summary, want) {
				t.Errorf("摘要應包含 %q:%q", want, summary)
			}
		}
		got := roundTripJSON(t, out)
		if got["data_kind"] != "qfii_holding_ranking" || got["is_realtime"] != false || got["data_as_of"] != nil || got["disclaimer"] != AnalysisDisclaimer {
			t.Errorf("structuredContent 共通欄位不正確:%v", got)
		}
		stock := got["stocks"].([]any)[0].(map[string]any)
		// JSON number 經 map[string]any 解析後是 float64;190 億在 float64
		// 可精確表示的整數範圍內,直接比較不會有精度問題。
		if stock["qfii_shares_held"] != 19000000000.0 || stock["qfii_share_holding_percentage"] != 73.2 || stock["issued_share"] != 25930000000.0 {
			t.Errorf("QFII fixture 未原樣保留:%v", stock)
		}
	})

	t.Run("查無資料:空陣列序列化為 [],摘要誠實說明且 QFII 仍標快照", func(t *testing.T) {
		f := &fakeMarketDataQuerier{index: emptyIndex, calendar: emptyCalendar, qfii: emptyQfii}
		mts := newMarketDataToolset(f)

		res, out, err := mts.marketIndexHistory(t.Context(), nil, IndexHistoryInput{})
		if err != nil {
			t.Fatalf("指數空資料不是錯誤:%v", err)
		}
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), `"points":[]`) || !strings.Contains(string(raw), `"data_as_of":null`) {
			t.Errorf("指數空資料語意錯誤:%s", raw)
		}
		if !strings.Contains(summaryOf(t, res), "沒有台股大盤指數資料") {
			t.Errorf("指數空資料摘要不誠實:%q", summaryOf(t, res))
		}

		res, out, err = mts.dividendCalendar(t.Context(), nil, DividendCalendarInput{})
		if err != nil {
			t.Fatalf("行事曆空資料不是錯誤:%v", err)
		}
		raw, _ = json.Marshal(out)
		if !strings.Contains(string(raw), `"events":[]`) {
			t.Errorf("行事曆空清單必須序列化為 []:%s", raw)
		}
		if !strings.Contains(summaryOf(t, res), "沒有股利行事曆事件") {
			t.Errorf("行事曆空資料摘要不誠實:%q", summaryOf(t, res))
		}

		res, out, err = mts.qfiiHoldingRanking(t.Context(), nil, QfiiRankingInput{})
		if err != nil {
			t.Fatalf("QFII 空資料不是錯誤:%v", err)
		}
		raw, _ = json.Marshal(out)
		if !strings.Contains(string(raw), `"stocks":[]`) || !strings.Contains(string(raw), `"data_as_of":null`) {
			t.Errorf("QFII 空資料語意錯誤:%s", raw)
		}
		if !strings.Contains(summaryOf(t, res), "快照") {
			t.Errorf("QFII 空資料摘要仍必須標明快照語意:%q", summaryOf(t, res))
		}
	})

	t.Run("底層錯誤只回安全通用訊息,不外洩內部細節", func(t *testing.T) {
		f := &fakeMarketDataQuerier{err: errors.New("dial tcp 10.0.0.1:9002: secret")}
		mts := newMarketDataToolset(f)
		calls := map[string]func() error{
			"index": func() error {
				_, _, err := mts.marketIndexHistory(t.Context(), nil, IndexHistoryInput{})
				return err
			},
			"calendar": func() error {
				_, _, err := mts.dividendCalendar(t.Context(), nil, DividendCalendarInput{})
				return err
			},
			"qfii": func() error {
				_, _, err := mts.qfiiHoldingRanking(t.Context(), nil, QfiiRankingInput{})
				return err
			},
		}
		for name, call := range calls {
			t.Run(name, func(t *testing.T) {
				err := call()
				if err == nil || err.Error() != errInternal || strings.Contains(err.Error(), "10.0.0.1") {
					t.Fatalf("內部錯誤必須安全:%v", err)
				}
			})
		}
	})
}

// TestMarketMoversTool 驗證排行工具的輸入驗證、預設值、三種來源摘要與
// structuredContent 的來源欄位。
func TestMarketMoversTool(t *testing.T) {
	// closingEnvelope 是「收盤來源」的固定回應;各子測試依需要調整
	// DataAsOf 來模擬「今天」與「前一交易日」兩種情境。
	closingEnvelope := func(dataAsOf string) *MarketMovers {
		return &MarketMovers{
			DataAsOf: dataAsOf, Source: "closing", IsRealtime: false,
			RankBy: "top_gainers", Market: "all",
			Movers: []Mover{{
				Rank: 1, StockSymbol: "2317", Name: "鴻海", MarketID: 2, IndustryID: 24,
				Price: ptr(250.0), ChangePercent: ptr(9.87), VolumeShares: ptr(123456789.0),
			}},
		}
	}

	t.Run("非法 rank_by 回傳可安全顯示的驗證錯誤", func(t *testing.T) {
		ts := newMarketDataToolset(&fakeMarketDataQuerier{})
		// top_trade_value 是刻意不提供的排序鍵,必須被擋下而不是猜測。
		for _, invalid := range []string{"top_trade_value", "gainers", "volume"} {
			_, _, err := ts.marketMovers(t.Context(), nil, MoversInput{RankBy: invalid})
			if err == nil || !strings.Contains(err.Error(), "rank_by") {
				t.Fatalf("%q 應回傳 rank_by 驗證錯誤,實際為:%v", invalid, err)
			}
		}
	})

	t.Run("大小寫與空白會被正規化", func(t *testing.T) {
		f := &fakeMarketDataQuerier{movers: closingEnvelope("2026-07-30")}
		ts := newMarketDataToolset(f)
		if _, _, err := ts.marketMovers(t.Context(), nil, MoversInput{RankBy: " TOP_LOSERS ", Market: " TWSE "}); err != nil {
			t.Fatalf("大小寫不同的合法值不應失敗:%v", err)
		}
		if f.gotMoversOpt.RankBy != "top_losers" || f.gotMoversOpt.Market != "twse" {
			t.Fatalf("正規化未生效:%#v", f.gotMoversOpt)
		}
	})

	t.Run("client 回傳空 envelope 時只回安全通用訊息", func(t *testing.T) {
		// 正常 client 不會回 (nil, nil),這裡驗證即使發生也不會 panic。
		ts := newMarketDataToolset(&fakeMarketDataQuerier{})
		_, _, err := ts.marketMovers(t.Context(), nil, MoversInput{})
		if err == nil || err.Error() != errInternal {
			t.Fatalf("空 envelope 必須回安全訊息:%v", err)
		}
	})

	t.Run("非法 market 與超界 limit 被擋下", func(t *testing.T) {
		ts := newMarketDataToolset(&fakeMarketDataQuerier{})
		if _, _, err := ts.marketMovers(t.Context(), nil, MoversInput{Market: "emerging"}); err == nil {
			t.Fatal("非法 market 應回傳錯誤")
		}
		if _, _, err := ts.marketMovers(t.Context(), nil, MoversInput{Limit: 51}); err == nil {
			t.Fatal("limit 超過 50 應回傳錯誤")
		}
	})

	t.Run("未提供參數時套用預設值", func(t *testing.T) {
		f := &fakeMarketDataQuerier{movers: closingEnvelope("2026-07-30")}
		ts := newMarketDataToolset(f)
		if _, _, err := ts.marketMovers(t.Context(), nil, MoversInput{}); err != nil {
			t.Fatalf("marketMovers() 不應失敗:%v", err)
		}
		if f.gotMoversOpt != (MoversOptions{RankBy: "top_gainers", Market: "all", Limit: 20}) {
			t.Fatalf("預設值未套用:%#v", f.gotMoversOpt)
		}
	})

	t.Run("收盤來源且資料日期非今日時明確標示前一交易日", func(t *testing.T) {
		f := &fakeMarketDataQuerier{movers: closingEnvelope("2026-07-30")}
		ts := newMarketDataToolset(f)
		res, structured, err := ts.marketMovers(t.Context(), nil, MoversInput{})
		if err != nil {
			t.Fatalf("marketMovers() 不應失敗:%v", err)
		}
		text := summaryOf(t, res)
		// 這是 13:30–15:00 空窗(以及假日)最關鍵的一句話。
		if !strings.Contains(text, "今日收盤資料尚未產生") || !strings.Contains(text, "2026-07-30") {
			t.Fatalf("摘要必須標示資料非今日:%q", text)
		}
		out, ok := structured.(MarketMoversOutput)
		if !ok {
			t.Fatalf("structuredContent 型別錯誤:%T", structured)
		}
		if out.DataKind != "market_movers" || out.IsRealtime || out.Source != "closing" {
			t.Fatalf("收盤來源的結構化輸出錯誤:%#v", out)
		}
		if out.SnapshotUpdatedAt != nil {
			t.Fatalf("收盤來源不得有快照時間:%#v", out.SnapshotUpdatedAt)
		}
		if !strings.Contains(text, AnalysisDisclaimer) {
			t.Fatalf("摘要必須含免責聲明:%q", text)
		}
	})

	t.Run("盤中即時來源標示即時與第三方來源警語", func(t *testing.T) {
		updatedAt := "2026-07-31T05:21:03+00:00"
		f := &fakeMarketDataQuerier{movers: &MarketMovers{
			DataAsOf: "2026-07-31", Source: "realtime", IsRealtime: true,
			RankBy: "top_volume", Market: "twse", SnapshotUpdatedAt: &updatedAt,
			Movers: []Mover{{
				Rank: 1, StockSymbol: "2330", Name: "台積電", MarketID: 2, IndustryID: 24,
				Price: ptr(1200.0), ChangePercent: ptr(2.56), VolumeLots: ptr(45000.0),
			}},
		}}
		ts := newMarketDataToolset(f)
		res, structured, err := ts.marketMovers(t.Context(), nil, MoversInput{RankBy: "top_volume", Market: "twse"})
		if err != nil {
			t.Fatalf("marketMovers() 不應失敗:%v", err)
		}
		text := summaryOf(t, res)
		if !strings.Contains(text, "盤中即時") || !strings.Contains(text, realtimeSourceNotice) {
			t.Fatalf("即時摘要必須標示盤中與第三方來源:%q", text)
		}
		// UTC 05:21 換算台北時間為 13:21。
		if !strings.Contains(text, "13:21") {
			t.Fatalf("即時摘要應以台北時間顯示快照時間:%q", text)
		}
		// 成交量榜的單位必須寫清楚:即時是「張」,與收盤的「股」差 1000 倍。
		if !strings.Contains(text, "張") {
			t.Fatalf("成交量榜摘要必須標明單位:%q", text)
		}
		out := structured.(MarketMoversOutput)
		if !out.IsRealtime || out.Source != "realtime" {
			t.Fatalf("即時來源必須誠實回報 is_realtime:%#v", out)
		}
	})

	t.Run("查無資料時仍附上來源說明與免責聲明", func(t *testing.T) {
		f := &fakeMarketDataQuerier{movers: &MarketMovers{
			DataAsOf: "2026-07-30", Source: "closing", RankBy: "top_gainers",
			Market: "tpex", Movers: []Mover{},
		}}
		ts := newMarketDataToolset(f)
		res, _, err := ts.marketMovers(t.Context(), nil, MoversInput{Market: "tpex"})
		if err != nil {
			t.Fatalf("空結果不是錯誤:%v", err)
		}
		text := summaryOf(t, res)
		if !strings.Contains(text, "沒有符合條件") || !strings.Contains(text, AnalysisDisclaimer) {
			t.Fatalf("空結果摘要錯誤:%q", text)
		}
	})

	t.Run("API 失敗時只回安全通用訊息", func(t *testing.T) {
		ts := newMarketDataToolset(&fakeMarketDataQuerier{err: errors.New("connection refused to 10.0.0.5:5432")})
		_, _, err := ts.marketMovers(t.Context(), nil, MoversInput{})
		if err == nil || err.Error() != errInternal {
			t.Fatalf("內部錯誤必須被遮蔽:%v", err)
		}
	})
}

// TestMoversSourceSummary 以固定時間點驗證三種來源情境的措辭。
//
// 直接測這個純函式,是因為「今天 vs 前一交易日」的判斷若依賴執行當下的
// 真實時鐘,測試會在跨日或不同時區的機器上隨機失敗。
func TestMoversSourceSummary(t *testing.T) {
	// 台北時間 2026-07-31 14:00(收盤後、收盤排程前的空窗時段)。
	now := time.Date(2026, 7, 31, 14, 0, 0, 0, taipeiLocation)
	updatedAt := "2026-07-31T03:30:00+00:00"

	cases := []struct {
		name     string
		envelope *MarketMovers
		wants    []string
		notWants []string
	}{
		{
			name:     "收盤且就是今天",
			envelope: &MarketMovers{DataAsOf: "2026-07-31", Source: "closing"},
			wants:    []string{"2026-07-31 收盤"},
			notWants: []string{"尚未產生"},
		},
		{
			name:     "收盤但資料停在前一交易日",
			envelope: &MarketMovers{DataAsOf: "2026-07-30", Source: "closing"},
			wants:    []string{"今日收盤資料尚未產生", "2026-07-30"},
		},
		{
			name:     "盤中即時且有快照時間",
			envelope: &MarketMovers{DataAsOf: "2026-07-31", Source: "realtime", IsRealtime: true, SnapshotUpdatedAt: &updatedAt},
			wants:    []string{"盤中即時", "11:30", realtimeSourceNotice},
		},
		{
			name:     "盤中即時但快照時間無法解析時不顯示時間",
			envelope: &MarketMovers{DataAsOf: "2026-07-31", Source: "realtime", IsRealtime: true, SnapshotUpdatedAt: ptrString("not-a-time")},
			wants:    []string{"盤中即時", realtimeSourceNotice},
			notWants: []string{"快照時間"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := moversSourceSummary(tc.envelope, now)
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("摘要缺少 %q:%q", want, got)
				}
			}
			for _, notWant := range tc.notWants {
				if strings.Contains(got, notWant) {
					t.Errorf("摘要不應包含 %q:%q", notWant, got)
				}
			}
		})
	}
}

// ptrString 是測試用的 *string 建構輔助函式。
func ptrString(v string) *string { return &v }

// TestMarketDataToolsAreReadOnly 經由真正 tools/list 驗證 Phase 4 每個
// 工具都帶 ReadOnlyHint,並確認 QFII 工具描述含快照限制(§4.10 要求
// 「寫入 tool 描述」,單測 handler 無法覆蓋註冊 metadata)。
func TestMarketDataToolsAreReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeMarketDataQuerier{}, nil)
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
	wants := map[string]bool{"get_market_index_history": false, "get_dividend_calendar": false, "get_qfii_holding_ranking": false, "get_market_movers": false}
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if _, ok := wants[tool.Name]; !ok {
			continue
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s 必須有 ReadOnlyHint=true", tool.Name)
		}
		if tool.Name == "get_qfii_holding_ranking" && (!strings.Contains(tool.Description, "快照") || !strings.Contains(tool.Description, "歷史序列")) {
			t.Errorf("QFII 工具描述必須標明快照與無歷史序列限制:%q", tool.Description)
		}
		// 排行工具的描述必須說明兩種資料來源,以及 13:30–15:00 空窗會拿到
		// 前一交易日資料這件事——LLM 只讀得到描述,漏寫就會誤答。
		if tool.Name == "get_market_movers" &&
			(!strings.Contains(tool.Description, "盤中") || !strings.Contains(tool.Description, "前一交易日")) {
			t.Errorf("排行工具描述必須說明盤中/收盤來源與空窗語意:%q", tool.Description)
		}
		wants[tool.Name] = true
	}
	for name, found := range wants {
		if !found {
			t.Errorf("tools/list 缺少 %s", name)
		}
	}
}
