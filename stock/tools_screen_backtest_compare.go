package stock

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 回測情境比較:同一組條件只改成交假設或部位設定,並排比較 2～4 個情境。

// BacktestScenarioResult 是一個情境的回測結果。
type BacktestScenarioResult struct {
	Label           string          `json:"label"`
	Overrides       string          `json:"overrides"`
	ExecutionModel  string          `json:"execution_model"`
	SlippageBps     int             `json:"slippage_bps"`
	FeeDiscount     float64         `json:"fee_discount"`
	EtfWeight       int             `json:"etf_weight"`
	RebalanceOffset int             `json:"rebalance_offset"`
	Summary         BacktestSummary `json:"summary"`
	Years           []BacktestYear  `json:"years"`
}

// ScreenBacktestCompareEnvelope 是 screen/backtest/compare endpoint 的完整回應。
type ScreenBacktestCompareEnvelope struct {
	Scenarios  []BacktestScenarioResult `json:"scenarios"`
	Disclaimer string                   `json:"disclaimer"`
}

// BacktestScenarioInput 是一個情境要覆蓋的參數;沒給的沿用共用條件。
type BacktestScenarioInput struct {
	Label           string   `json:"label,omitempty"`
	ExecutionPrice  string   `json:"execution_price,omitempty"`
	SlippageBps     *int     `json:"slippage_bps,omitempty"`
	FeeDiscount     *float64 `json:"fee_discount,omitempty"`
	EtfWeight       *int     `json:"etf_weight,omitempty"`
	MaxWeight       *int     `json:"max_weight,omitempty"`
	KeepRank        *int     `json:"keep_rank,omitempty"`
	RebalanceOffset *int     `json:"rebalance_offset,omitempty"`
}

// ScreenBacktestCompareInput 是 compare_screen_backtests 的輸入:共用回測條件加上 2～4 個情境。
type ScreenBacktestCompareInput struct {
	ScreenBacktestInput
	Scenarios []BacktestScenarioInput `json:"scenarios"`
}

// ScreenBacktestCompareOutput 是比較 envelope 加上 MCP 分析中繼資料。
type ScreenBacktestCompareOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*ScreenBacktestCompareEnvelope
}

// ScreenBacktestCompareQuerier 是 compare_screen_backtests 對資料來源的需求介面;只有 *APIClient 實作。
type ScreenBacktestCompareQuerier interface {
	ScreenBacktestCompare(ctx context.Context, opt ScreenBacktestOptions, scenarios []url.Values) (*ScreenBacktestCompareEnvelope, error)
}

// ScreenBacktestCompare 查詢情境比較;每個情境的覆蓋參數編碼成一個 scenario 參數。
func (c *APIClient) ScreenBacktestCompare(ctx context.Context, opt ScreenBacktestOptions, scenarios []url.Values) (*ScreenBacktestCompareEnvelope, error) {
	values := opt.query()
	for _, scenario := range scenarios {
		values.Add("scenario", scenario.Encode())
	}
	var body ScreenBacktestCompareEnvelope
	if err := c.getBacktest(ctx, "/api/v1/stocks/screen/backtest/compare?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// scenarioValues 驗證一個情境並轉成覆蓋參數:套用到共用輸入後要通過與 get_screen_backtest 相同的驗證。
func scenarioValues(base ScreenBacktestInput, s BacktestScenarioInput, index int) (url.Values, error) {
	merged := base
	values := url.Values{}
	if s.ExecutionPrice != "" {
		merged.ExecutionPrice = s.ExecutionPrice
		values.Set("execution_price", s.ExecutionPrice)
	}
	setInt := func(key string, value *int, target *int) {
		if value != nil {
			*target = *value
			values.Set(key, strconv.Itoa(*value))
		}
	}
	setInt("slippage_bps", s.SlippageBps, &merged.SlippageBps)
	setInt("etf_weight", s.EtfWeight, &merged.EtfWeight)
	setInt("max_weight", s.MaxWeight, &merged.MaxWeight)
	setInt("keep_rank", s.KeepRank, &merged.KeepRank)
	setInt("rebalance_offset", s.RebalanceOffset, &merged.RebalanceOffset)
	if s.FeeDiscount != nil {
		merged.FeeDiscount = s.FeeDiscount
		values.Set("fee_discount", strconv.FormatFloat(*s.FeeDiscount, 'f', -1, 64))
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("第 %d 個情境沒有指定任何要改的參數", index+1)
	}
	if _, err := screenBacktestOptions(merged); err != nil {
		return nil, fmt.Errorf("第 %d 個情境:%w", index+1, err)
	}
	if label := strings.TrimSpace(s.Label); label != "" {
		if len([]rune(label)) > 40 {
			return nil, fmt.Errorf("第 %d 個情境的 label 不可超過 40 字", index+1)
		}
		values.Set("label", label)
	}
	return values, nil
}

// screenBacktestCompareSchema 是共用回測條件加上 scenarios。
func screenBacktestCompareSchema() *jsonschema.Schema {
	schema := screenBacktestSchema()
	optionalInt := func(description string, minimum, maximum float64) *jsonschema.Schema {
		return &jsonschema.Schema{Type: "integer", Minimum: ptr(minimum), Maximum: ptr(maximum), Description: description}
	}
	schema.Properties["scenarios"] = &jsonschema.Schema{
		Type:        "array",
		MinItems:    ptr(2),
		MaxItems:    ptr(4),
		Description: "2 至 4 個情境(必填):每個情境只寫要改的參數,其餘沿用上面的共用條件;例如 [{label:收盤},{label:次日開盤+20bps,execution_price:next_open,slippage_bps:20}]",
		Items: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"label":            {Type: "string", MaxLength: ptr(40), Description: "情境名稱(選填)"},
				"execution_price":  {Type: "string", Enum: []any{"close", "next_open"}, Description: "成交價模型"},
				"slippage_bps":     optionalInt("滑價(基點)", 0, 200),
				"fee_discount":     {Type: "number", Minimum: ptr(0.0), Maximum: ptr(1.0), Description: "手續費折扣"},
				"etf_weight":       optionalInt("0050 核心部位(%)", 0, 100),
				"max_weight":       optionalInt("單檔權重上限(%)", 1, 100),
				"keep_rank":        optionalInt("換股緩衝名次", 1, 200),
				"rebalance_offset": optionalInt("換股日偏移", 0, 239),
			},
			AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		},
	}
	schema.Required = append(schema.Required, "scenarios")
	return schema
}

// screenBacktestCompareToolset 綁定比較資料來源與記錄函式。
type screenBacktestCompareToolset struct {
	cq   ScreenBacktestCompareQuerier
	logf func(string, ...any)
}

func (ts *screenBacktestCompareToolset) screenBacktestCompare(ctx context.Context, _ *mcp.CallToolRequest, in ScreenBacktestCompareInput) (*mcp.CallToolResult, any, error) {
	if len(in.Scenarios) < 2 || len(in.Scenarios) > 4 {
		return nil, nil, fmt.Errorf("參數 scenarios 必須有 2 到 4 個情境")
	}
	opt, err := screenBacktestOptions(in.ScreenBacktestInput)
	if err != nil {
		return nil, nil, err
	}
	scenarios := make([]url.Values, 0, len(in.Scenarios))
	for i, scenario := range in.Scenarios {
		values, err := scenarioValues(in.ScreenBacktestInput, scenario, i)
		if err != nil {
			return nil, nil, err
		}
		scenarios = append(scenarios, values)
	}
	envelope, err := ts.cq.ScreenBacktestCompare(ctx, opt, scenarios)
	if err != nil {
		return nil, nil, backtestToolError(err, "compare_screen_backtests", opt, ts.logf)
	}
	return textResult(screenBacktestCompareSummary(envelope, opt)), ScreenBacktestCompareOutput{
		DataKind: "screen_backtest_compare", IsRealtime: false, Disclaimer: AnalysisDisclaimer, ScreenBacktestCompareEnvelope: envelope,
	}, nil
}

// screenBacktestCompareSummary 組出給模型閱讀的文字摘要:各情境的關鍵指標與相對第一個情境的差異。
func screenBacktestCompareSummary(e *ScreenBacktestCompareEnvelope, opt ScreenBacktestOptions) string {
	var b summaryBuilder
	b.printf("回測情境比較:%s;共 %d 個情境,各自扣交易成本:", backtestOptionsText(opt), len(e.Scenarios))
	for i, s := range e.Scenarios {
		dd := s.Summary.DailyMaxDrawdownAfterCosts
		basis := "日頻"
		if dd == nil {
			dd, basis = s.Summary.MaxDrawdownAfterCosts, "期末"
		}
		b.printf("\n- %s(%s、滑價 %d bps、0050 %d%%):%d 期,年化 %s、夏普 %s、最大回撤 %s(%s),0050 同期年化 %s",
			s.Label, executionModelText(s.ExecutionModel), s.SlippageBps, s.EtfWeight, s.Summary.Periods,
			displayPercent(s.Summary.AnnualizedAfterCosts), displayFloat(s.Summary.SharpeRatio), displayPercent(dd), basis,
			displayPercent(s.Summary.EtfAnnualizedReturn))
		if i > 0 && e.Scenarios[0].Summary.AnnualizedAfterCosts != nil && s.Summary.AnnualizedAfterCosts != nil {
			b.printf(";年化比「%s」%+.2f 個百分點", e.Scenarios[0].Label,
				*s.Summary.AnnualizedAfterCosts-*e.Scenarios[0].Summary.AnnualizedAfterCosts)
		}
	}
	b.printf("\n過去表現不代表未來結果,亦不會自動下單。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// executionModelText 是成交價模型的中文說明。
func executionModelText(model string) string {
	if model == "next_open" {
		return "次日開盤成交"
	}
	return "換股日收盤成交"
}
