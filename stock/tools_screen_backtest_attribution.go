package stock

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 回測報酬歸因:把策略相對類 0050 基準的超額報酬拆成產業配置、選股與交互三項(Brinson-Fachler)。

// BacktestIndustryEffect 是一個產業(或現金)的歸因效果;效果為各期相加的百分點,不複利、未扣成本。
type BacktestIndustryEffect struct {
	Industry           string  `json:"industry"`
	AvgWeight          float64 `json:"avg_weight"`
	BenchmarkAvgWeight float64 `json:"benchmark_avg_weight"`
	Allocation         float64 `json:"allocation"`
	Selection          float64 `json:"selection"`
	Interaction        float64 `json:"interaction"`
	Total              float64 `json:"total"`
}

// BacktestAttributionTotals 是一段期間的歸因合計;EtfReturn 在全部缺 0050 資料時為 nil。
type BacktestAttributionTotals struct {
	Periods         int      `json:"periods"`
	StrategyReturn  float64  `json:"strategy_return"`
	BenchmarkReturn float64  `json:"benchmark_return"`
	EtfReturn       *float64 `json:"etf_return"`
	ExcessReturn    float64  `json:"excess_return"`
	Allocation      float64  `json:"allocation"`
	Selection       float64  `json:"selection"`
	Interaction     float64  `json:"interaction"`
}

// BacktestAttributionYear 是一年的歸因合計與影響最大的產業。
type BacktestAttributionYear struct {
	Year       int                       `json:"year"`
	Totals     BacktestAttributionTotals `json:"totals"`
	Industries []BacktestIndustryEffect  `json:"industries"`
}

// ScreenBacktestAttributionEnvelope 是 screen/backtest/attribution endpoint 的完整回應。
type ScreenBacktestAttributionEnvelope struct {
	From            string                    `json:"from"`
	To              string                    `json:"to"`
	Market          string                    `json:"market"`
	HoldDays        int                       `json:"hold_days"`
	Limit           int                       `json:"limit"`
	RankBy          *string                   `json:"rank_by"`
	Weighting       *string                   `json:"weighting"`
	MaxWeight       *int                      `json:"max_weight"`
	StopLoss        *int                      `json:"stop_loss"`
	TrailingStop    *int                      `json:"trailing_stop"`
	RebalanceOffset int                       `json:"rebalance_offset"`
	BenchmarkSize   int                       `json:"benchmark_size"`
	Summary         BacktestAttributionTotals `json:"summary"`
	Years           []BacktestAttributionYear `json:"years"`
	Industries      []BacktestIndustryEffect  `json:"industries"`
}

// ScreenBacktestAttributionQuerier 是 get_screen_backtest_attribution 對資料來源的需求介面;只有 *APIClient 實作。
type ScreenBacktestAttributionQuerier interface {
	ScreenBacktestAttribution(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestAttributionEnvelope, error)
}

// ScreenBacktestAttribution 查詢回測報酬歸因;etf_weight 與 split 不使用(只看策略部位)。
// 404 代表迄日之前沒有相對強度資料(或具名策略不存在),422 代表產業名稱不存在。
func (c *APIClient) ScreenBacktestAttribution(ctx context.Context, opt ScreenBacktestOptions) (*ScreenBacktestAttributionEnvelope, error) {
	values := opt.query()
	values.Del("etf_weight")
	values.Del("split")
	var body ScreenBacktestAttributionEnvelope
	if err := c.getBacktest(ctx, "/api/v1/stocks/screen/backtest/attribution?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// ScreenBacktestAttributionOutput 是歸因 envelope 加上 MCP 分析中繼資料。
type ScreenBacktestAttributionOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*ScreenBacktestAttributionEnvelope
}

// screenBacktestAttributionSchema 與 get_screen_backtest 相同,但不收 etf_weight(歸因只看策略部位)。
func screenBacktestAttributionSchema() *jsonschema.Schema {
	schema := screenBacktestSchema()
	delete(schema.Properties, "etf_weight")
	return schema
}

// screenBacktestAttributionToolset 綁定歸因資料來源與記錄函式。
type screenBacktestAttributionToolset struct {
	aq   ScreenBacktestAttributionQuerier
	logf func(string, ...any)
}

// screenBacktestAttribution 執行 get_screen_backtest_attribution;參數驗證與 get_screen_backtest 相同,etf_weight 與 split 忽略。
func (ts *screenBacktestAttributionToolset) screenBacktestAttribution(ctx context.Context, _ *mcp.CallToolRequest, in ScreenBacktestInput) (*mcp.CallToolResult, any, error) {
	in.EtfWeight, in.Split = 0, ""
	opt, err := screenBacktestOptions(in)
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.aq.ScreenBacktestAttribution(ctx, opt)
	if err != nil {
		return nil, nil, backtestToolError(err, "get_screen_backtest_attribution", opt, ts.logf)
	}
	return textResult(screenBacktestAttributionSummary(envelope, opt)), ScreenBacktestAttributionOutput{
		DataKind: "screen_backtest_attribution", IsRealtime: false, Disclaimer: AnalysisDisclaimer, ScreenBacktestAttributionEnvelope: envelope,
	}, nil
}

// effectText 是一個產業的三項效果與權重。
func effectText(item BacktestIndustryEffect) string {
	return fmt.Sprintf("%s 合計 %+.2f(配置 %+.2f、選股 %+.2f、交互 %+.2f;平均權重 %.2f%% vs 基準 %.2f%%)",
		item.Industry, item.Total, item.Allocation, item.Selection, item.Interaction, item.AvgWeight, item.BenchmarkAvgWeight)
}

// totalsText 是一段期間的合計。
func totalsText(t BacktestAttributionTotals) string {
	etf := ""
	if t.EtfReturn != nil {
		etf = fmt.Sprintf("、0050 %+.2f", *t.EtfReturn)
	}
	return fmt.Sprintf("%d 期,策略 %+.2f、基準 %+.2f%s,超額 %+.2f = 配置 %+.2f + 選股 %+.2f + 交互 %+.2f",
		t.Periods, t.StrategyReturn, t.BenchmarkReturn, etf, t.ExcessReturn, t.Allocation, t.Selection, t.Interaction)
}

// attributionListedIndustries 是摘要列出的整段產業數(依合計由大到小的前幾名與最拖累的幾名)。
const attributionListedIndustries = 5

// screenBacktestAttributionSummary 組出給模型閱讀的文字摘要:設定、整段與逐年合計、貢獻與拖累最多的產業。
func screenBacktestAttributionSummary(e *ScreenBacktestAttributionEnvelope, opt ScreenBacktestOptions) string {
	var b summaryBuilder
	b.printf("%s 市場 %s～%s 回測報酬歸因(換股日偏移 %d):%s;基準為每個換股日近 20 個交易日平均成交金額最高的 %d 檔、依成交金額加權(近似 0050 的市值加權),只看策略部位、未扣成本,各期效果直接相加(百分點,不複利)。",
		e.Market, e.From, e.To, e.RebalanceOffset, backtestOptionsText(opt), e.BenchmarkSize)
	if e.Summary.Periods == 0 {
		b.printf("\n期間內沒有可歸因的期數。")
	} else {
		b.printf("\n整段:%s。", totalsText(e.Summary))
		if len(e.Years) > 0 {
			b.printf("\n依年度(每年列影響最大的產業):")
			for _, year := range e.Years {
				b.printf("\n- %d 年:%s", year.Year, totalsText(year.Totals))
				if len(year.Industries) > 0 {
					b.printf(";主要:%s", effectText(year.Industries[0]))
				}
			}
		}
		top := e.Industries[:min(attributionListedIndustries, len(e.Industries))]
		b.printf("\n貢獻最多的產業:")
		for _, item := range top {
			b.printf("\n- %s", effectText(item))
		}
		if len(e.Industries) > attributionListedIndustries {
			bottom := e.Industries[max(attributionListedIndustries, len(e.Industries)-attributionListedIndustries):]
			b.printf("\n拖累最多的產業:")
			for i := len(bottom) - 1; i >= 0; i-- {
				b.printf("\n- %s", effectText(bottom[i]))
			}
		}
	}
	b.printf("\n配置效果:多配或少配某產業 × 該產業基準報酬相對基準整體的差(例如少配大漲的半導體為負);選股效果:以基準權重計,同產業選到的股票相對基準同產業股票的報酬差;交互效果:多配或少配的部分 × 選股的報酬差。策略的現金(大盤濾網空手、單檔上限)算一個報酬 0 的「現金」類別。基準與 0050 的差距見各期 0050 報酬。過去表現不代表未來結果。免責聲明:%s",
		AnalysisDisclaimer)
	return b.String()
}
