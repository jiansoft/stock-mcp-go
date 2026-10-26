package stock

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 0050 核心部位前緣:同一組回測條件搭配 0%～100% 的 0050,看各比例的報酬與風險。

// BacktestFrontierPoint 是一個 0050 核心部位比例下,各換股日偏移績效的分布。
type BacktestFrontierPoint struct {
	EtfWeight    int                         `json:"etf_weight"`
	Distribution BacktestOffsetDistribution  `json:"distribution"`
	BeforeSplit  *BacktestOffsetDistribution `json:"before_split"`
	AfterSplit   *BacktestOffsetDistribution `json:"after_split"`
}

// ScreenBacktestFrontierEnvelope 是 screen/backtest/frontier endpoint 的完整回應。
type ScreenBacktestFrontierEnvelope struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	Market      string   `json:"market"`
	HoldDays    int      `json:"hold_days"`
	Limit       int      `json:"limit"`
	RankBy      *string  `json:"rank_by"`
	Weighting   *string  `json:"weighting"`
	MaxWeight   *int     `json:"max_weight"`
	FeeDiscount *float64 `json:"fee_discount"`
	// ExecutionModel 是成交價模型(close 或 next_open);SlippageBps 是買賣各計的滑價(基點)。舊版 API 沒有這兩欄時為 nil。
	ExecutionModel   *string `json:"execution_model"`
	SlippageBps      *int    `json:"slippage_bps"`
	EtfBenchmark     *string `json:"etf_benchmark"`
	Offsets          int     `json:"offsets"`
	BestSharpeWeight *int    `json:"best_sharpe_weight"`
	BestReturnWeight *int    `json:"best_return_weight"`
	// Split 與 BestSharpeWeightBeforeSplit 只有指定 split 時才有。
	Split                       *string                 `json:"split"`
	BestSharpeWeightBeforeSplit *int                    `json:"best_sharpe_weight_before_split"`
	Weights                     []BacktestFrontierPoint `json:"weights"`
}

// ScreenBacktestFrontierQuerier 是 get_screen_backtest_frontier 對資料來源的需求介面;只有 *APIClient 實作。
type ScreenBacktestFrontierQuerier interface {
	ScreenBacktestFrontier(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestFrontierEnvelope, error)
}

// ScreenBacktestFrontier 查詢 0050 核心部位前緣;rebalance_offset 與 etf_weight 不使用(由 API 跑全部比例與偏移)。
// 404 代表迄日之前沒有相對強度資料,422 代表產業名稱不存在。
func (c *APIClient) ScreenBacktestFrontier(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestFrontierEnvelope, error) {
	values := opt.query()
	values.Del("rebalance_offset")
	values.Del("etf_weight")
	var body ScreenBacktestFrontierEnvelope
	if err := c.getBacktest(ctx, "/api/v1/stocks/screen/backtest/frontier?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// ScreenBacktestFrontierOutput 是前緣 envelope 加上 MCP 分析中繼資料。
type ScreenBacktestFrontierOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*ScreenBacktestFrontierEnvelope
}

// screenBacktestFrontierSchema 與 get_screen_backtest_offsets 相同,但不收 etf_weight。
func screenBacktestFrontierSchema() *jsonschema.Schema {
	schema := screenBacktestOffsetsSchema()
	delete(schema.Properties, "etf_weight")
	return schema
}

// screenBacktestFrontierToolset 綁定前緣資料來源與記錄函式。
type screenBacktestFrontierToolset struct {
	fq   ScreenBacktestFrontierQuerier
	logf func(string, ...any)
}

// screenBacktestFrontier 執行 get_screen_backtest_frontier;參數驗證與 get_screen_backtest 相同,
// rebalance_offset 與 etf_weight 忽略。
func (ts *screenBacktestFrontierToolset) screenBacktestFrontier(ctx context.Context, _ *mcp.CallToolRequest, in ScreenBacktestInput) (*mcp.CallToolResult, any, error) {
	in.RebalanceOffset, in.EtfWeight = 0, 0
	opt, err := screenBacktestOptions(in)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.fq.ScreenBacktestFrontier(ctx, opt)
	if err != nil {
		return nil, nil, backtestToolError(err, "get_screen_backtest_frontier", opt, ts.logf)
	}
	return textResult(screenBacktestFrontierSummary(envelope, opt)), ScreenBacktestFrontierOutput{
		DataKind: "screen_backtest_frontier", IsRealtime: false, Disclaimer: AnalysisDisclaimer, ScreenBacktestFrontierEnvelope: envelope,
	}, nil
}

// statMedian 回分布中位數的指標;nil 回 nil。
func statMedian(stat *BacktestStat) *float64 {
	if stat == nil {
		return nil
	}
	return &stat.Median
}

// weightText 把比例轉成「N%」;nil 回「無」。
func weightText(weight *int) string {
	if weight == nil {
		return "無"
	}
	return fmt.Sprintf("%d%%", *weight)
}

// screenBacktestFrontierSummary 組出給模型閱讀的文字摘要:設定、各比例的中位數與最佳比例。
func screenBacktestFrontierSummary(e *ScreenBacktestFrontierEnvelope, opt ScreenBacktestOptions) string {
	var b summaryBuilder
	b.printf("%s 市場 %s～%s 0050 核心部位前緣:%s;每個比例都把換股日偏移 %d 種跑一遍,扣交易成本(手續費折扣 %s)後各偏移的中位數:",
		e.Market, e.From, e.To, backtestOptionsText(opt), e.Offsets, displayFloat(e.FeeDiscount))
	for _, point := range e.Weights {
		d := point.Distribution
		b.printf("\n- 0050 %d%%:年化 %s、夏普 %s、最大回撤 %s、波動率 %s;勝過 0050 的偏移比例:報酬 %s、夏普 %s",
			point.EtfWeight, displayPercent(statMedian(d.AnnualizedReturnAfterCosts)), displayFloat(statMedian(d.SharpeRatio)),
			displayPercent(statMedian(d.MaxDrawdownAfterCosts)), displayPercent(statMedian(d.Volatility)),
			displayRate(d.BeatEtfReturnRate), displayRate(d.BeatEtfSharpeRate))
		if point.AfterSplit != nil {
			b.printf(";樣本外 年化 %s、夏普 %s", backtestMedianText(point.AfterSplit.AnnualizedReturnAfterCosts),
				displayFloat(statMedian(point.AfterSplit.SharpeRatio)))
		}
	}
	if len(e.Weights) > 0 {
		etf := e.Weights[0].Distribution
		b.printf("\n同期 0050(同樣的換股日進出、未扣成本)中位數:年化 %s、夏普 %s、最大回撤 %s。",
			displayPercent(statMedian(etf.EtfAnnualizedReturn)), displayFloat(statMedian(etf.EtfSharpeRatio)), displayPercent(statMedian(etf.EtfMaxDrawdown)))
	}
	b.printf("\n夏普值中位數最高的比例:%s;年化報酬中位數最高的比例:%s。", weightText(e.BestSharpeWeight), weightText(e.BestReturnWeight))
	if e.Split != nil {
		b.printf("\n樣本內外以 %s 分割(換股日早於這天為樣本內):只看樣本內時夏普值中位數最高的比例 %s;上面各比例的「樣本外」是分割日之後的表現,可看樣本內挑的比例到樣本外是否仍然好。",
			*e.Split, weightText(e.BestSharpeWeightBeforeSplit))
	}
	b.printf("\n0%% 為純策略、100%% 等於只持有 0050;每期調回固定比例的買賣計入成本(0050 證交稅 0.1%%,第一次買進 0050 不計)。各比例用同一批回測,最佳比例仍可能只是這段期間的結果。過去表現不代表未來結果。免責聲明:%s",
		AnalysisDisclaimer)
	return b.String()
}
