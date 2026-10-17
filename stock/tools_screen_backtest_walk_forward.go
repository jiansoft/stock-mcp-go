package stock

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 滾動前推最佳化:每年只用之前的資料挑參數再套用到這一年,看「每年重新調參數」在沒看過的年份能不能勝過 0050。

// BacktestWalkForwardYear 是滾動前推的一年:用前幾年挑出的參數與這一年的結果。
type BacktestWalkForwardYear struct {
	Year                     int      `json:"year"`
	EtfWeight                int      `json:"etf_weight"`
	MaxWeight                *int     `json:"max_weight"`
	KeepRank                 int      `json:"keep_rank"`
	TrainSharpeRatio         float64  `json:"train_sharpe_ratio"`
	Periods                  int      `json:"periods"`
	ReturnAfterCosts         float64  `json:"return_after_costs"`
	BaselineReturnAfterCosts float64  `json:"baseline_return_after_costs"`
	EtfReturn                *float64 `json:"etf_return"`
}

// ScreenBacktestWalkForwardEnvelope 是 screen/backtest/walk-forward endpoint 的完整回應。
type ScreenBacktestWalkForwardEnvelope struct {
	From            string                     `json:"from"`
	To              string                     `json:"to"`
	Market          string                     `json:"market"`
	HoldDays        int                        `json:"hold_days"`
	Limit           int                        `json:"limit"`
	RankBy          *string                    `json:"rank_by"`
	Weighting       *string                    `json:"weighting"`
	StopLoss        *int                       `json:"stop_loss"`
	TrailingStop    *int                       `json:"trailing_stop"`
	FeeDiscount     *float64                   `json:"fee_discount"`
	EtfBenchmark    *string                    `json:"etf_benchmark"`
	TrainYears      int                        `json:"train_years"`
	Candidates      int                        `json:"candidates"`
	RebalanceOffset int                        `json:"rebalance_offset"`
	Years           []BacktestWalkForwardYear  `json:"years"`
	Summary         BacktestSummary            `json:"summary"`
	Baseline        BacktestSummary            `json:"baseline"`
	Offsets         BacktestOffsetDistribution `json:"offsets"`
	BaselineOffsets BacktestOffsetDistribution `json:"baseline_offsets"`
}

// ScreenBacktestWalkForwardQuerier 是 get_screen_backtest_walk_forward 對資料來源的需求介面;只有 *APIClient 實作。
type ScreenBacktestWalkForwardQuerier interface {
	ScreenBacktestWalkForward(ctx context.Context, opt ScreenBacktestOptions, trainYears int) (*ScreenBacktestWalkForwardEnvelope, error)
}

// ScreenBacktestWalkForward 查詢滾動前推最佳化;split 不使用,trainYears 為 0 時用 API 預設(3)。
// 404 代表迄日之前沒有相對強度資料(或具名策略不存在),422 代表產業名稱不存在或期間沒有超過訓練年數。
func (c *APIClient) ScreenBacktestWalkForward(ctx context.Context, opt ScreenBacktestOptions, trainYears int) (*ScreenBacktestWalkForwardEnvelope, error) {
	values := opt.query()
	values.Del("split")
	if trainYears > 0 {
		values.Set("train_years", fmt.Sprint(trainYears))
	}
	var body ScreenBacktestWalkForwardEnvelope
	if err := c.getBacktest(ctx, "/api/v1/stocks/screen/backtest/walk-forward?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// ScreenBacktestWalkForwardInput 是 get_screen_backtest_walk_forward 的輸入:回測條件加上訓練年數。
type ScreenBacktestWalkForwardInput struct {
	ScreenBacktestInput
	TrainYears int `json:"train_years,omitempty"`
}

// ScreenBacktestWalkForwardOutput 是滾動前推 envelope 加上 MCP 分析中繼資料。
type ScreenBacktestWalkForwardOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*ScreenBacktestWalkForwardEnvelope
}

// screenBacktestWalkForwardSchema 與 get_screen_backtest 相同,另收 train_years。
func screenBacktestWalkForwardSchema() *jsonschema.Schema {
	schema := screenBacktestSchema()
	schema.Properties["train_years"] = &jsonschema.Schema{
		Type:        "integer",
		Minimum:     ptr(1.0),
		Maximum:     ptr(5.0),
		Default:     []byte("3"),
		Description: "訓練年數(選填,1 至 5,預設 3):每年年初只用之前這麼多年的期數挑參數,套用到這一年;回測期間必須超過訓練年數",
	}
	return schema
}

// screenBacktestWalkForwardToolset 綁定滾動前推資料來源與記錄函式。
type screenBacktestWalkForwardToolset struct {
	wq   ScreenBacktestWalkForwardQuerier
	logf func(string, ...any)
}

// screenBacktestWalkForward 執行 get_screen_backtest_walk_forward;參數驗證與 get_screen_backtest 相同,split 忽略。
func (ts *screenBacktestWalkForwardToolset) screenBacktestWalkForward(ctx context.Context, _ *mcp.CallToolRequest, in ScreenBacktestWalkForwardInput) (*mcp.CallToolResult, any, error) {
	if in.TrainYears != 0 && (in.TrainYears < 1 || in.TrainYears > 5) {
		return nil, nil, fmt.Errorf("參數 train_years 必須介於 1 到 5 之間")
	}
	in.Split = ""
	opt, err := screenBacktestOptions(in.ScreenBacktestInput)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.wq.ScreenBacktestWalkForward(ctx, opt, in.TrainYears)
	if err != nil {
		return nil, nil, backtestToolError(err, "get_screen_backtest_walk_forward", opt, ts.logf)
	}
	return textResult(screenBacktestWalkForwardSummary(envelope, opt)), ScreenBacktestWalkForwardOutput{
		DataKind: "screen_backtest_walk_forward", IsRealtime: false, Disclaimer: AnalysisDisclaimer, ScreenBacktestWalkForwardEnvelope: envelope,
	}, nil
}

// summaryLine 是一個彙總的扣成本後年化報酬、夏普值與最大回撤。
func summaryLine(s BacktestSummary) string {
	return fmt.Sprintf("%d 期,年化 %s、夏普 %s、最大回撤 %s;0050 同期年化 %s、夏普 %s",
		s.Periods, displayPercent(s.AnnualizedAfterCosts), displayFloat(s.SharpeRatio), displayPercent(s.MaxDrawdownAfterCosts),
		displayPercent(s.EtfAnnualizedReturn), displayFloat(s.EtfSharpeRatio))
}

// screenBacktestWalkForwardSummary 組出給模型閱讀的文字摘要:逐年挑出的參數、滾動與不調參數的彙總與各偏移中位數。
func screenBacktestWalkForwardSummary(e *ScreenBacktestWalkForwardEnvelope, opt ScreenBacktestOptions) string {
	var b summaryBuilder
	b.printf("%s 市場 %s～%s 滾動前推最佳化:%s;每年年初只用之前 %d 年的期數,在 %d 組參數(0050 核心部位 × 單檔上限 × 換股緩衝)中挑扣成本後夏普值最高的一組套用到這一年(手續費折扣 %s)。",
		e.Market, e.From, e.To, backtestOptionsText(opt), e.TrainYears, e.Candidates, displayFloat(e.FeeDiscount))
	if len(e.Years) == 0 {
		b.printf("\n沒有可套用的年份(訓練期間期數不足)。")
	} else {
		b.printf("\n逐年(換股日偏移 %d):", e.RebalanceOffset)
		for _, y := range e.Years {
			etf := ""
			if y.EtfReturn != nil {
				etf = "、0050 " + displayPercent(y.EtfReturn)
			}
			b.printf("\n- %d 年:0050 %d%%、單檔上限 %s、緩衝前 %d 名(訓練夏普 %.2f)→ %d 期扣成本 %s(不調參數 %s%s)",
				y.Year, y.EtfWeight, weightText(y.MaxWeight), y.KeepRank, y.TrainSharpeRatio, y.Periods,
				displayPercent(&y.ReturnAfterCosts), displayPercent(&y.BaselineReturnAfterCosts), etf)
		}
		b.printf("\n串起樣本外年份:滾動調參數 %s。", summaryLine(e.Summary))
		b.printf("\n同樣年份不調參數:%s。", summaryLine(e.Baseline))
	}
	b.printf("\n各換股日偏移的中位數:滾動 年化 %s、夏普 %s(勝過 0050 的偏移比例:報酬 %s、夏普 %s);不調參數 年化 %s、夏普 %s(勝過 0050:報酬 %s、夏普 %s)。",
		backtestMedianText(e.Offsets.AnnualizedReturnAfterCosts), displayFloat(statMedian(e.Offsets.SharpeRatio)),
		displayRate(e.Offsets.BeatEtfReturnRate), displayRate(e.Offsets.BeatEtfSharpeRate),
		backtestMedianText(e.BaselineOffsets.AnnualizedReturnAfterCosts), displayFloat(statMedian(e.BaselineOffsets.SharpeRatio)),
		displayRate(e.BaselineOffsets.BeatEtfReturnRate), displayRate(e.BaselineOffsets.BeatEtfSharpeRate))
	b.printf("\n滾動調參數比不調參數差,代表事後挑的參數在當時其實挑不出來;兩者都輸 0050 時,這組條件本身沒有優勢。各組合的成本是自己整段連續持有算出來的,換參數當下的調整成本不另計。過去表現不代表未來結果。免責聲明:%s",
		AnalysisDisclaimer)
	return b.String()
}
