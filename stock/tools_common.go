package stock

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 各組工具共用的參數驗證、文字摘要格式化與共用 schema。

// datePattern 用來檢查日期字串的「格式」是否為四位數字-二位數字-二位
// 數字,例如 "2026-07-13"。這只驗證格式,不驗證日期是否真實存在(例如
// "2026-13-40" 會通過這個正則表達式,但不是一個真實存在的日期)——
// 真實性檢查交給 time.Parse 處理,見 parseDateArg。
var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// validateLength 以 Unicode 字元數(而非 byte 數)驗證字串長度。
func validateLength(value, field string, minLen, maxLen int) error {
	n := utf8.RuneCountInString(value)
	if n < minLen || n > maxLen {
		return fmt.Errorf("參數 %s 長度必須介於 %d 到 %d 字元之間,目前為 %d 字元", field, minLen, maxLen, n)
	}
	return nil
}

// normalizeSymbol 驗證並正規化股票代號:先確認長度(trim 前後空白後
// 1 到 24 字元),再統一轉成大寫(例如 "00631l" 轉成 "00631L")。
//
// 把「正規化規則」集中寫在這一個函式,是為了確保「拿去查資料庫的值」跟
// 「查不到時回錯誤訊息用的值」永遠是同一套規則處理出來的結果,不會有
// 兩處各自轉換、卻不小心寫得不一致的風險。
func normalizeSymbol(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if err := validateLength(s, "symbol", 1, 24); err != nil {
		return "", err
	}
	return strings.ToUpper(s), nil
}

// rangedLimit 套用 limit 參數的預設值與範圍限制。
//
// value 等於 0 時視為「呼叫端沒有提供這個參數」——這個判斷方式依賴一個
// Go 特性:int 型別在 JSON 解析時,如果 JSON 裡沒有這個欄位,對應的
// struct 欄位會維持 Go 的零值(int 的零值就是 0),所以「沒提供」跟
// 「使用者明確傳了 0」在這裡是無法分辨的兩種情況——但因為業務邏輯上
// 「查詢筆數上限是 0」本來就沒有意義(規格書要求範圍是 1 起跳),把
// 兩者都視為「使用預設值」不會造成誤判。
//
// 超出範圍時明確回傳錯誤,而不是靜默把值夾到邊界(例如把 999 硬夾成
// 50)——如果靜默夾邊界,使用者可能誤以為自己輸入的數字真的被採用了,
// 明確報錯比較誠實,也能讓使用者立刻發現輸入有誤。
func rangedLimit(value, def, min, max int) (int, error) {
	if value == 0 {
		return def, nil
	}
	if value < min || value > max {
		return 0, fmt.Errorf("參數 limit 必須介於 %d 到 %d 之間,收到了 %d", min, max, value)
	}
	return value, nil
}

// parseDateArg 解析並驗證 YYYY-MM-DD 格式的日期字串;空字串代表呼叫端
// 沒有提供這個參數,回傳 (nil, nil)。
//
// 這裡分兩層驗證:先用 datePattern 這個正則表達式檢查「格式」對不對
// (四位數-二位數-二位數),格式對了才進一步用 time.Parse 檢查「日期
// 是否真實存在」——例如 "2026-13-40" 符合正則表達式的格式,但 13 月、
// 40 日並不是真實存在的日期,time.Parse 在這種情況下會回傳錯誤,被
// 這個函式攔下來轉成使用者看得懂的訊息。
func parseDateArg(raw, field string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	if !datePattern.MatchString(raw) {
		return nil, fmt.Errorf("參數 %s 日期格式必須為 YYYY-MM-DD", field)
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, fmt.Errorf("參數 %s 日期格式必須為 YYYY-MM-DD", field)
	}
	return &t, nil
}

// ---------------------------------------------------------------------------
// 工具實作
// ---------------------------------------------------------------------------
//
// 以下四個方法是四個 MCP 工具真正的執行邏輯,簽名都符合 go-sdk 要求的
// ToolHandlerFor[In, Out] 泛型形狀:
//
//	func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error)
//
// 回傳值有三個:
//   - *mcp.CallToolResult:給人類/LLM 讀的文字摘要(這裡都是呼叫
//     textResult 組出來的),structuredContent 由 SDK 依第二個回傳值
//     自動填入,不需要手動組裝。
//   - Out(這裡型別是 any):structuredContent 實際要輸出的資料;SDK 看到
//     這裡不是 nil,會自動序列化並放進 CallToolResult.StructuredContent。
//   - error:非 nil 時,SDK 會自動把整個結果轉成「isError: true」的
//     MCP 工具錯誤(見本檔案開頭的說明),因此這裡回傳的每一個 error
//     訊息都必須是可以安全顯示給呼叫端的文字。

// textResult 建立只含一段文字摘要的 mcp.CallToolResult。
//
// structuredContent 欄位刻意不在這裡設定:go-sdk 的 AddTool 機制會在
// handler 回傳後,依第二個回傳值(每個工具方法回傳的 Out,例如
// SearchStockOutput)自動序列化並填入 StructuredContent,本函式只需要
// 負責「文字摘要」這一半。
func textResult(summary string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: summary}},
	}
}

// summaryBuilder 累積工具回應的人類可讀摘要。
//
// 它存在的唯一理由是 fmt.Fprintf 的 error 回傳值。摘要是寫進
// strings.Builder 的,而 strings.Builder.Write 的文件明確保證「回傳的
// error 永遠是 nil」——換句話說這裡的寫入在型別上會回傳 error,實際上
// 卻不可能失敗。若在每個呼叫點都寫成 `_, _ = fmt.Fprintf(&b, ...)`,
// 十幾行摘要組裝的邏輯會被這個純粹的儀式性前綴淹沒;若原樣不管,靜態
// 分析又會對每一行報 unhandled error。
//
// 因此把「不可能失敗」這件事集中在這裡宣告一次:呼叫端維持乾淨的
// b.printf(...),而錯誤被明確丟棄的位置只有下面這兩行,一眼就能看見
// 並驗證其正確性。
type summaryBuilder struct {
	b strings.Builder
}

func (s *summaryBuilder) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(&s.b, format, args...)
}

func (s *summaryBuilder) writeString(text string) {
	_, _ = s.b.WriteString(text)
}

func (s *summaryBuilder) String() string { return s.b.String() }

// displayFloat 把 *float64 格式化成摘要文字給人類閱讀:nil 顯示「無」,
// 避免摘要裡出現像 Go 內部表示法的 "<nil>" 這種對一般使用者毫無意義的
// 字樣。這個函式只用在「文字摘要」(content),structuredContent 裡的
// 數字欄位仍然維持 *float64(nil 就是 nil),兩者互不影響。
func displayFloat(v *float64) string {
	if v == nil {
		return "無"
	}
	return formatFloat(*v)
}

// displayString 對 *string 做跟 displayFloat 相同的「nil 顯示無」處理。
func displayString(v *string) string {
	if v == nil {
		return "無"
	}
	return *v
}

// formatFloat 以「最短可還原表示法」把 float64 轉成字串,例如輸出
// "123.45" 而不是 "123.450000"。
//
// strconv.FormatFloat 的第三個參數(這裡是 -1)是「有效位數」設定,傳
// -1 時 Go 會採用剛好足夠精確重建原始浮點數所需的最少位數,不會像
// fmt.Sprintf("%f", v) 那樣固定輸出六位小數、產生一堆多餘的尾隨零。
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// ---------------------------------------------------------------------------
// 跨 Phase 共用的市場與日期參數處理
// ---------------------------------------------------------------------------
//
// 以下幾個函式最初是為 Phase 2 寫的,但 Phase 3(選股)與 Phase 4(QFII
// 排行、指數歷史)後來也用到同一套規則,因此集中放在本檔案——共用的東西
// 放在共用的地方,才不會讓「選股」的實作莫名其妙依賴「估值分析」那個
// 檔案裡的函式。

// breadthMarketSchema 描述市場廣度統計表本身的市場列。
//
// `all` 必須忠實描述 Data API 查詢的 id 0 合併列；它和排行／選股直接
// 過濾 stocks 表的 `IN (2, 4)` 不是同一種底層語意，因此不可共用說明。
func breadthMarketSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        []any{"all", "twse", "tpex"},
		Default:     []byte(`"all"`),
		Description: "統計列:all(市場 id 0 的全市場合併列)、twse(上市 id 2)或 tpex(上櫃 id 4)",
	}
}

// listedOTCMarketSchema 描述直接過濾股票主檔的上市櫃市場範圍。
// `all` 固定為上市 id 2 加上櫃 id 4，不包含公開發行與興櫃。
func listedOTCMarketSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        []any{"all", "twse", "tpex"},
		Default:     []byte(`"all"`),
		Description: "市場:all(上市+上櫃，不含公開發行與興櫃)、twse(上市)或 tpex(上櫃)",
	}
}

// normalizeMarket 套用預設市場並拒絕固定白名單以外的值。
func normalizeMarket(raw string) (string, error) {
	if raw == "" {
		return "all", nil
	}
	market := strings.ToLower(strings.TrimSpace(raw))
	if market != "all" && market != "twse" && market != "tpex" {
		return "", fmt.Errorf("參數 market 必須為 all、twse 或 tpex,收到了 %q", raw)
	}
	return market, nil
}

// parseOptionalDate 驗證日期並保留 Data API 需要的 YYYY-MM-DD 字串。
func parseOptionalDate(raw, field string) (string, error) {
	parsed, err := parseDateArg(raw, field)
	if err != nil || parsed == nil {
		return raw, err
	}
	return parsed.Format("2006-01-02"), nil
}
