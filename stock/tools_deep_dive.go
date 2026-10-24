package stock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/url"
	"sync"
	"time"
)

// StockDeepDive 是一次取得的多面向歷史資料；各區塊保留原始資料日與單位。
type StockDeepDive struct {
	DataKind    string                     `json:"data_kind"`
	StockSymbol string                     `json:"stock_symbol"`
	IsRealtime  bool                       `json:"is_realtime"`
	Disclaimer  string                     `json:"disclaimer"`
	Sections    map[string]json.RawMessage `json:"sections"`
	Warnings    map[string]string          `json:"warnings"`
	// failures 保留各區塊的實際錯誤供呼叫端記錄 log，不輸出給模型，避免洩漏內部細節。
	failures map[string]error
}

// DeepDiveQuerier 提供個股資料聚合，僅具備此能力時才註冊複合工具。
type DeepDiveQuerier interface {
	AnalyzeStockDeepDive(context.Context, string) (*StockDeepDive, error)
}

// AnalyzeStockDeepDive 先確認股票存在，再以最多四個並行請求取得九個分析區塊。
// 單一區塊失敗仍回傳其他結果，整體最多等待 25 秒；錯誤細節不外洩。
func (c *APIClient) AnalyzeStockDeepDive(ctx context.Context, symbol string) (*StockDeepDive, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	profile, err := c.StockProfile(ctx, symbol)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, ErrStockNotFound
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return nil, err
	}
	result := &StockDeepDive{DataKind: "stock_deep_dive", StockSymbol: symbol, Disclaimer: AnalysisDisclaimer,
		Sections: map[string]json.RawMessage{"profile": raw}, Warnings: map[string]string{}, failures: map[string]error{}}
	sections := map[string]string{
		"valuation_band": "valuation-band?years=5", "chip": "chip?days=5",
		"financial_statements": "financial-statements?period_type=quarterly&limit=8",
		"monthly_revenues":     "monthly-revenues?limit=12", "financial_reports": "financial-reports?period_type=quarterly&limit=8",
		"financial_health": "financial-health", "technicals": "technicals?days=20",
		"dividends": "dividends?limit=10", "valuation": "valuation",
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	slots := make(chan struct{}, 4)
	for name, suffix := range sections {
		wg.Go(func() {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				result.Warnings[name] = deepDiveCancelled
				mu.Unlock()
				return
			}
			defer func() { <-slots }()
			var body json.RawMessage
			found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/"+suffix, &body)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil && ctx.Err() != nil:
				result.Warnings[name] = deepDiveCancelled
				result.failures[name] = err
			case err != nil:
				result.Warnings[name] = "此區塊目前無法取得"
				result.failures[name] = err
			case !found:
				result.Warnings[name] = "此區塊查無資料"
			default:
				result.Sections[name] = body
			}
		})
	}
	wg.Wait()
	// 整體逾時或被取消且沒有任何分析區塊成功（只剩基本資料）時，回報錯誤而不是看似成功的空結果。
	if err := ctx.Err(); err != nil && len(result.Sections) == 1 {
		return nil, err
	}
	return result, nil
}

// deepDiveCancelled 是整體逾時或查詢被取消時的區塊警示，等待中與已送出的請求共用同一句。
const deepDiveCancelled = "查詢已取消或逾時"

// addDeepDiveTool 註冊唯讀聚合工具，避免模型逐一呼叫各分析工具。
func addDeepDiveTool(server *mcp.Server, q Querier, logf func(string, ...any)) {
	dq, ok := q.(DeepDiveQuerier)
	if !ok {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "analyze_stock_deep_dive", Description: "一次查詢個股完整分析：報價與基本面、五年估值區間、五日籌碼、營收、三率、三大財報、財務體質、技術與股利。部分缺資料會列警示。", InputSchema: symbolOnlySchema(), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SymbolInput) (*mcp.CallToolResult, any, error) {
			symbol, err := normalizeSymbol(in.Symbol)
			if err != nil {
				return nil, nil, err
			}
			result, err := dq.AnalyzeStockDeepDive(ctx, symbol)
			if errors.Is(err, ErrStockNotFound) {
				return nil, nil, fmt.Errorf("查無股票 %s", symbol)
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				logf("深度分析 %s 逾時或已取消:%v", symbol, err)
				return nil, nil, fmt.Errorf("查詢逾時或已取消，請稍後再試")
			}
			if err != nil {
				logf("深度分析查詢失敗:%v", err)
				return nil, nil, fmt.Errorf("%s", errInternal)
			}
			if result == nil {
				return nil, nil, fmt.Errorf("%s", errInternal)
			}
			for name, failure := range result.failures {
				logf("深度分析 %s 區塊 %s 失敗:%v", symbol, name, failure)
			}
			return textResult(fmt.Sprintf("%s 深度分析：取得 %d 個資料區塊，%d 個區塊未能取得。各區塊的資料日與原始單位保留於結構化資料。%s", symbol, len(result.Sections), len(result.Warnings), AnalysisDisclaimer)), result, nil
		})
}
