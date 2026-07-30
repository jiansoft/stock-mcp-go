package stock

// 本檔案只做一件事:用編譯期斷言把「哪個實作應該滿足哪些能力介面」這件
// 事釘死。這裡不含任何執行期邏輯,產生的執行檔也不會多出任何東西。
//
// ## 為什麼這幾行很重要?
//
// AddTools 是用型別斷言做能力偵測的:
//
//	if financials, ok := q.(FinancialQuerier); ok { ...註冊三個工具... }
//
// 這個寫法的好處是「資料來源不支援某組能力時,就不會對使用者暴露一定會
// 失敗的工具」;但它有一個危險的副作用——型別斷言失敗時是**完全靜默**
// 的。如果有人改動了 *APIClient 上某個方法的名稱、參數或回傳型別,讓它
// 不再滿足 FinancialQuerier,結果會是:
//
//   - 程式照常編譯通過,沒有任何錯誤或警告。
//   - 服務照常啟動,沒有任何 log。
//   - 只是 tools/list 裡默默少了三個工具,使用者「問了但 LLM 說沒有這個
//     功能」,而且沒有任何線索指向真正的原因。
//
// var _ Interface = (*Type)(nil) 這個慣用寫法把這種靜默失敗轉成編譯期
// 錯誤:宣告一個捨棄名稱(_)的變數,型別是介面,值是該型別的 nil 指標。
// 編譯器必須驗證這個指標型別確實實作了該介面,不符合就直接編譯失敗。
// 因為值是 nil 且變數名稱是 _,不會有任何配置或執行期成本。
var (
	// 兩種資料來源都必須滿足四個核心工具所需的最小介面。
	_ Querier = (*Repository)(nil)
	_ Querier = (*APIClient)(nil)

	// 以下各組能力目前只有 *APIClient 具備(db 模式為過渡期的比對用途,
	// 刻意不實作,因此 db 模式下這些工具不會出現在 tools/list)。
	_ SnapshotQuerier   = (*APIClient)(nil)
	_ FinancialQuerier  = (*APIClient)(nil)
	_ AnalyticsQuerier  = (*APIClient)(nil)
	_ Screener          = (*APIClient)(nil)
	_ MarketDataQuerier = (*APIClient)(nil)

	// 健康檢查兩種資料來源都必須支援,/readyz 才能真實反映後端狀態。
	_ HealthChecker = (*Repository)(nil)
	_ HealthChecker = (*APIClient)(nil)
)
