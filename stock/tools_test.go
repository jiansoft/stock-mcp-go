package stock

// 本檔案測試四個 MCP 工具(searchStock、latestDailyQuote、priceHistory、
// stockProfile)的輸入驗證與輸出組裝邏輯,完全不連真實資料庫——這正是
// tools.go 把 Querier 定義成介面帶來的好處(見 tools.go 開頭的說明):
// 這裡注入 fakeQuerier 這個手寫的假實作,測試可以精確控制「資料庫查詢
// 回傳什麼」,專注驗證 tools.go 自己的邏輯(驗證規則、data_as_of 判斷、
// 摘要文字組裝)有沒有寫對,不會被真實資料庫的連線速度或資料變動影響。

import (
	"context"
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
