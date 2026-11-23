package stock

import (
	"context"
	"fmt"
	"reflect"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"stockmcp/apikey"
)

// 工具政策（會員系統計畫 6.3）：每個工具註冊時必須宣告政策，包裝層統一以呼叫端 Principal 的 grants 檢查；
// Personal 工具會讀寫個人資料，member_id 一律經 resolveMemberID（4-03）。

// 權限碼（與 stock_go 的權限目錄一致）。
const (
	PermMarketView       = "market.view"
	PermAnalysisRun      = "analysis.run"
	PermPortfolioView    = "portfolio.view"
	PermPortfolioEdit    = "portfolio.edit"
	PermMoneyHistoryView = "money_history.view"
	PermInventoryView    = "inventory.view"
)

// ToolPolicy 是一個工具需要的權限，以及是否讀寫個人資料。
type ToolPolicy struct {
	Permission string
	Personal   bool
}

func market() ToolPolicy   { return ToolPolicy{Permission: PermMarketView} }
func analysis() ToolPolicy { return ToolPolicy{Permission: PermAnalysisRun} }
func personal(permission string) ToolPolicy {
	return ToolPolicy{Permission: permission, Personal: true}
}

// toolPolicies 是全部工具的政策表；不在表內的工具註冊時直接 panic（無法啟動）。
var toolPolicies = map[string]ToolPolicy{
	// 持股類（15 個 Personal）：11 個 portfolio.view、每日市值 money_history.view、已領股息 inventory.view、2 個寫入
	"get_portfolio_summary":             personal(PermPortfolioView),
	"get_holding_chip":                  personal(PermPortfolioView),
	"get_holding_dividend_fill":         personal(PermPortfolioView),
	"get_portfolio_etf_exposure":        personal(PermPortfolioView),
	"get_portfolio_rebalance":           personal(PermPortfolioView),
	"get_portfolio_targets":             personal(PermPortfolioView),
	"list_portfolio_target_revisions":   personal(PermPortfolioView),
	"get_dividend_forecast":             personal(PermPortfolioView),
	"get_dividend_forecast_accuracy":    personal(PermPortfolioView),
	"get_upcoming_dividends":            personal(PermPortfolioView),
	"get_upcoming_payouts":              personal(PermPortfolioView),
	"get_portfolio_history":             personal(PermMoneyHistoryView),
	"get_dividend_income":               personal(PermInventoryView),
	"set_portfolio_targets":             personal(PermPortfolioEdit),
	"restore_portfolio_target_revision": personal(PermPortfolioEdit),

	// 重運算（Data API 路徑含 /backtest，2.3）：analysis.run，並向 stock_go 請領租約（4-04）
	"get_screen_backtest":              analysis(),
	"get_screen_backtest_offsets":      analysis(),
	"compare_screen_backtests":         analysis(),
	"get_screen_backtest_frontier":     analysis(),
	"get_screen_backtest_walk_forward": analysis(),
	"get_screen_backtest_attribution":  analysis(),

	// 一般市場查詢：market.view
	"analyze_stock_deep_dive":              market(),
	"compare_etfs":                         market(),
	"get_cagr_ranking":                     market(),
	"get_chip_data":                        market(),
	"get_dividend_calendar":                market(),
	"get_dividend_fill":                    market(),
	"get_dividend_fill_stats":              market(),
	"get_dividend_history":                 market(),
	"get_dividend_yield_ranking":           market(),
	"get_etf_detail":                       market(),
	"get_etf_holding_changes":              market(),
	"get_etf_holding_history":              market(),
	"get_etf_proxy_tracking":               market(),
	"get_financial_health":                 market(),
	"get_financial_reports":                market(),
	"get_financial_statement_history":      market(),
	"get_holder_levels":                    market(),
	"get_industry_flow":                    market(),
	"get_industry_performance":             market(),
	"get_industry_strength":                market(),
	"get_institutional_flow_ranking":       market(),
	"get_institutional_streak_performance": market(),
	"get_latest_daily_quote":               market(),
	"get_margin_ranking":                   market(),
	"get_market_breadth":                   market(),
	"get_market_forward_returns":           market(),
	"get_market_index_history":             market(),
	"get_market_movers":                    market(),
	"get_market_regime":                    market(),
	"get_market_valuation":                 market(),
	"get_monthly_revenue_history":          market(),
	"get_price_history":                    market(),
	"get_qfii_holding_ranking":             market(),
	"get_realtime_snapshot":                market(),
	"get_relative_strength_history":        market(),
	"get_relative_strength_ranking":        market(),
	"get_signal_performance":               market(),
	"get_signal_stocks":                    market(),
	"get_stock_cagr":                       market(),
	"get_stock_peers":                      market(),
	"get_stock_profile":                    market(),
	"get_stock_signal_history":             market(),
	"get_stock_timeline":                   market(),
	"get_stock_valuation":                  market(),
	"get_strategy_edge":                    market(),
	"get_strategy_track":                   market(),
	"get_technical_indicators":             market(),
	"get_valuation_band":                   market(),
	"list_strategy_presets":                market(),
	"screen_canslim":                       market(),
	"screen_etfs":                          market(),
	"screen_stocks":                        market(),
	"search_stock":                         market(),
}

// PolicyOf 回傳工具的政策；未宣告時第二個回傳值為 false。
func PolicyOf(name string) (ToolPolicy, bool) {
	policy, ok := toolPolicies[name]
	return policy, ok
}

// requirePrincipal 為 true 時，沒有呼叫端身分的工具呼叫一律拒絕（正式環境由 main 開啟；
// MCP handler 前一律有 API key 驗證，身分經 auth.TokenInfo 帶入）。測試以記憶體傳輸直接呼叫時為 false。
var requirePrincipal atomic.Bool

// RequirePrincipal 開啟「沒有呼叫端身分即拒絕」。
func RequirePrincipal() { requirePrincipal.Store(true) }

// SetRequirePrincipal 設定「沒有呼叫端身分即拒絕」並回傳原本的值。
// 這是行程層級的開關；測試以記憶體傳輸直接呼叫工具時用它關閉，結束時還原，避免受其他測試執行順序影響。
func SetRequirePrincipal(on bool) bool { return requirePrincipal.Swap(on) }

// principalExtraKey 是 auth.TokenInfo.Extra 中存放呼叫端 Principal 的鍵。
const principalExtraKey = "principal"

// TokenInfoPrincipal 把 Principal 放進 auth.TokenInfo.Extra 的鍵名（供 main 的驗證器使用）。
const TokenInfoPrincipal = principalExtraKey

type principalContextKey struct{}

// principalFrom 取出包裝層放進 context 的呼叫端身分。
func principalFrom(ctx context.Context) (apikey.Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(apikey.Principal)
	return p, ok
}

// requestPrincipal 從工具請求的 TokenInfo 取出呼叫端身分。
func requestPrincipal(req *mcp.CallToolRequest) (apikey.Principal, bool) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return apikey.Principal{}, false
	}
	p, ok := req.Extra.TokenInfo.Extra[principalExtraKey].(apikey.Principal)
	return p, ok
}

// authorizeTool 依政策檢查呼叫端：個人 Token 以 grants[政策權限] 判定（none 或缺少即拒絕）；
// 尚未套用會員權限的舊金鑰（Legacy 且沒有 grants）沿用改版前的行為。
func authorizeTool(name string, policy ToolPolicy, principal apikey.Principal, found bool) error {
	if !found {
		if requirePrincipal.Load() {
			return fmt.Errorf("呼叫 %s 需要通過驗證的 API key", name)
		}
		return nil
	}
	if principal.Legacy && principal.Grants == nil {
		return nil
	}
	if scope := principal.Grants[policy.Permission]; scope == "" || scope == "none" {
		return fmt.Errorf("此 Token 沒有 %s 權限，無法使用 %s", policy.Permission, name)
	}
	return nil
}

// registerTool 註冊工具並包上政策檢查：工具名稱必須在政策表內，否則 panic（未宣告政策的工具無法註冊）。
// 檢查通過後把呼叫端身分放進 context，供 resolveMemberID 等使用。
func registerTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	policy, ok := toolPolicies[tool.Name]
	if !ok {
		panic(fmt.Sprintf("工具 %s 沒有宣告政策（stock/policy.go 的 toolPolicies）", tool.Name))
	}
	field, hasMember := memberIDField(reflect.TypeFor[In]())
	if policy.Personal && !hasMember {
		panic(fmt.Sprintf("Personal 工具 %s 的輸入缺少 int64 的 member_id 欄位", tool.Name))
	}
	mcp.AddTool(server, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		principal, found := requestPrincipal(req)
		if err := authorizeTool(tool.Name, policy, principal, found); err != nil {
			return nil, zero, err
		}
		if found {
			ctx = context.WithValue(ctx, principalContextKey{}, principal)
		}
		if policy.Personal {
			var err error
			if ctx, err = scopePersonal(ctx, req, &in, field, policy.Permission, principal, found); err != nil {
				return nil, zero, err
			}
		}
		return handler(ctx, req, in)
	})
}
