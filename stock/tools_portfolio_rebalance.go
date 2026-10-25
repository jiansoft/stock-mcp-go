// Package stock 的持股再平衡建議工具: 提供目標配置設定與再平衡試算。
//
// 呼叫 stock_rust Data API 的 GET /api/v1/portfolio/rebalance 與 PUT/GET /api/v1/portfolio/targets。
// 本工具僅提供試算與偏離提示，絕不下單交易。所有結果附帶免責聲明。
package stock

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RebalanceDisclaimer 是持股再平衡試算的免責聲明文字。
const RebalanceDisclaimer = "試算結果僅供參考，不構成任何投資建議。"

// TargetDisclaimer 是目標配置設定的免責聲明文字。
const TargetDisclaimer = "目標配置比例僅為個人偏好試算基準，非自動交易委託。"

// StockRebalanceItem 是個股維度再平衡建議項目。
type StockRebalanceItem struct {
	Symbol           string  `json:"symbol"`
	Name             string  `json:"name"`
	CurrentShares    int64   `json:"current_shares"`
	CurrentPrice     float64 `json:"current_price"`
	CurrentValue     float64 `json:"current_value"`
	CurrentWeight    float64 `json:"current_weight"`
	TargetWeight     float64 `json:"target_weight"`
	DeviationPercent float64 `json:"deviation_percent"`
	Action           string  `json:"action"` // "buy", "sell", "hold"
	SuggestedShares  int64   `json:"suggested_shares"`
	SuggestedAmount  float64 `json:"suggested_amount"`
	EstimatedCost    float64 `json:"estimated_cost"`
	NetAmount        float64 `json:"net_amount"`
}

// IndustryRebalanceItem 是產業維度再平衡建議項目。
type IndustryRebalanceItem struct {
	Industry         string   `json:"industry"`
	CurrentValue     float64  `json:"current_value"`
	CurrentWeight    float64  `json:"current_weight"`
	TargetWeight     float64  `json:"target_weight"`
	DeviationPercent float64  `json:"deviation_percent"`
	CandidateStocks  []string `json:"candidate_stocks"`
}

// EtfLookThroughRebalanceItem 是 ETF 穿透維度再平衡建議項目。
type EtfLookThroughRebalanceItem struct {
	StockSymbol      string  `json:"stock_symbol"`
	Name             string  `json:"name"`
	DirectValue      float64 `json:"direct_value"`
	IndirectValue    float64 `json:"indirect_value"`
	TotalValue       float64 `json:"total_value"`
	CurrentWeight    float64 `json:"current_weight"`
	TargetWeight     float64 `json:"target_weight"`
	DeviationPercent float64 `json:"deviation_percent"`
}

// RebalanceCashSummary 是再平衡現金流試算摘要。
type RebalanceCashSummary struct {
	InitialCash            float64 `json:"initial_cash"`
	EstimatedSellNet       float64 `json:"estimated_sell_net"`
	EstimatedBuyTotal      float64 `json:"estimated_buy_total"`
	AvailableCapital       float64 `json:"available_capital"`
	RequiredAdditionalCash float64 `json:"required_additional_cash"`
	RemainingCash          float64 `json:"remaining_cash"`
}

// PortfolioRebalance 是持股再平衡完整試算結果。
type PortfolioRebalance struct {
	MemberID            int64                         `json:"member_id"`
	Dimension           string                        `json:"dimension"`
	TotalEquityValue    float64                       `json:"total_equity_value"`
	TotalPortfolioValue float64                       `json:"total_portfolio_value"`
	TargetCashPercent   float64                       `json:"target_cash_percent"`
	TolerancePercent    float64                       `json:"tolerance_percent"`
	LotSize             int                           `json:"lot_size"`
	StockItems          []StockRebalanceItem          `json:"stock_items"`
	IndustryItems       []IndustryRebalanceItem       `json:"industry_items"`
	EtfItems            []EtfLookThroughRebalanceItem `json:"etf_items"`
	CashSummary         RebalanceCashSummary          `json:"cash_summary"`
	Disclaimer          string                        `json:"disclaimer"`
}

// PortfolioTargetItem 是目標配置項目。
type PortfolioTargetItem struct {
	TargetKey     string  `json:"target_key"`
	TargetPercent float64 `json:"target_percent"`
}

// PortfolioTargets 是目標配置設定結果。
type PortfolioTargets struct {
	MemberID          int64                 `json:"member_id"`
	Dimension         string                `json:"dimension"`
	Targets           []PortfolioTargetItem `json:"targets"`
	TargetCashPercent float64               `json:"target_cash_percent"`
	Disclaimer        string                `json:"disclaimer"`
}

// RebalanceOptions 是呼叫 GET /api/v1/portfolio/rebalance 的選項。
type RebalanceOptions struct {
	MemberID         int64
	Dimension        string
	LotSize          int
	TolerancePercent float64
	AvailableCash    float64
	FeeDiscount      float64
}

// SetTargetsOptions 是呼叫 PUT /api/v1/portfolio/targets 的選項。
type SetTargetsOptions struct {
	MemberID  int64
	Dimension string
	Targets   []PortfolioTargetItem
}

// PortfolioRebalanceQuerier 是持股再平衡與目標配置對資料來源的需求介面。
type PortfolioRebalanceQuerier interface {
	PortfolioRebalance(ctx context.Context, opt RebalanceOptions) (*PortfolioRebalance, error)
	SetPortfolioTargets(ctx context.Context, opt SetTargetsOptions) (*PortfolioTargets, error)
	GetPortfolioTargets(ctx context.Context, memberID int64, dimension string) (*PortfolioTargets, error)
}

// PortfolioRebalance 呼叫 Data API 查詢持股再平衡建議。
func (c *APIClient) PortfolioRebalance(ctx context.Context, opt RebalanceOptions) (*PortfolioRebalance, error) {
	values := url.Values{
		"dimension":         []string{opt.Dimension},
		"lot_size":          []string{fmt.Sprint(opt.LotSize)},
		"tolerance_percent": []string{fmt.Sprint(opt.TolerancePercent)},
		"available_cash":    []string{fmt.Sprint(opt.AvailableCash)},
	}
	if opt.MemberID > 0 {
		values.Set("member_id", fmt.Sprint(opt.MemberID))
	}
	if opt.FeeDiscount > 0 {
		values.Set("fee_discount", fmt.Sprint(opt.FeeDiscount))
	}
	var body PortfolioRebalance
	found, err := c.getFound(ctx, "/api/v1/portfolio/rebalance?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("尚未設定此維度的目標配置比例，請先使用 set_portfolio_targets 設定目標")
	}
	if body.StockItems == nil {
		body.StockItems = []StockRebalanceItem{}
	}
	if body.IndustryItems == nil {
		body.IndustryItems = []IndustryRebalanceItem{}
	}
	if body.EtfItems == nil {
		body.EtfItems = []EtfLookThroughRebalanceItem{}
	}
	return &body, nil
}

// SetPortfolioTargets 呼叫 Data API 設定目標配置比例。
func (c *APIClient) SetPortfolioTargets(ctx context.Context, opt SetTargetsOptions) (*PortfolioTargets, error) {
	payload := struct {
		MemberID  int64                 `json:"member_id"`
		Dimension string                `json:"dimension"`
		Targets   []PortfolioTargetItem `json:"targets"`
	}{
		MemberID:  opt.MemberID,
		Dimension: opt.Dimension,
		Targets:   opt.Targets,
	}
	var body PortfolioTargets
	if err := c.put(ctx, "/api/v1/portfolio/targets", payload, &body); err != nil {
		return nil, err
	}
	if body.Targets == nil {
		body.Targets = []PortfolioTargetItem{}
	}
	return &body, nil
}

// GetPortfolioTargets 呼叫 Data API 查詢目前目標配置設定。
func (c *APIClient) GetPortfolioTargets(ctx context.Context, memberID int64, dimension string) (*PortfolioTargets, error) {
	values := url.Values{"member_id": []string{fmt.Sprint(memberID)}}
	if dimension != "" {
		values.Set("dimension", dimension)
	}
	var body PortfolioTargets
	if err := c.get(ctx, "/api/v1/portfolio/targets?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Targets == nil {
		body.Targets = []PortfolioTargetItem{}
	}
	return &body, nil
}

// RebalanceInput 是 get_portfolio_rebalance 的輸入參數。
type RebalanceInput struct {
	MemberID         int64   `json:"member_id"`
	Dimension        string  `json:"dimension"`
	LotSize          int     `json:"lot_size"`
	TolerancePercent float64 `json:"tolerance_percent"`
	AvailableCash    float64 `json:"available_cash"`
	FeeDiscount      float64 `json:"fee_discount"`
}

// SetTargetsInput 是 set_portfolio_targets 的輸入參數。
type SetTargetsInput struct {
	MemberID  int64                 `json:"member_id"`
	Dimension string                `json:"dimension"`
	Targets   []PortfolioTargetItem `json:"targets"`
}

// GetTargetsInput 是 get_portfolio_targets 的輸入參數。
type GetTargetsInput struct {
	MemberID  int64  `json:"member_id"`
	Dimension string `json:"dimension"`
}

// PortfolioRebalanceOutput 是 get_portfolio_rebalance 的結構化輸出。
type PortfolioRebalanceOutput struct {
	DataKind   string `json:"data_kind"`
	Disclaimer string `json:"disclaimer"`
	*PortfolioRebalance
}

// PortfolioTargetsOutput 是 set/get_portfolio_targets 的結構化輸出。
type PortfolioTargetsOutput struct {
	DataKind   string `json:"data_kind"`
	Disclaimer string `json:"disclaimer"`
	*PortfolioTargets
}

func portfolioRebalanceSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"dimension": {
				Type:        "string",
				Enum:        []any{"stock", "industry", "etf_look_through"},
				Default:     []byte(`"stock"`),
				Description: "再平衡維度: stock(個股)、industry(產業)、etf_look_through(ETF穿透)，預設 stock",
			},
			"lot_size": {
				Type:        "integer",
				Enum:        []any{1, 1000},
				Default:     []byte("1000"),
				Description: "交易單位股數: 1000(整張，預設) 或 1(零股)",
			},
			"tolerance_percent": {
				Type:        "number",
				Minimum:     ptr(0.0),
				Default:     []byte("2.0"),
				Description: "容忍帶百分比(±% 偏離內不建議交易)，預設 2.0%",
			},
			"available_cash": {
				Type:        "number",
				Minimum:     ptr(0.0),
				Default:     []byte("0.0"),
				Description: "帳戶目前可用現金(元)，預設 0.0",
			},
			"fee_discount": {
				Type:        "number",
				Minimum:     ptr(0.0),
				Maximum:     ptr(1.0),
				Default:     []byte("1.0"),
				Description: "券商手續費折讓(例如 0.28 代表 28 折，1.0 為無折讓)，預設 1.0",
			},
			"member_id": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Default:     []byte("0"),
				Description: "會員編號; 0(預設)為全部部位",
			},
		},
	}
}

func setPortfolioTargetsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"dimension", "targets"},
		Properties: map[string]*jsonschema.Schema{
			"dimension": {
				Type:        "string",
				Enum:        []any{"stock", "industry", "etf_look_through"},
				Description: "目標配置維度: stock、industry、etf_look_through",
			},
			"targets": {
				Type:        "array",
				Description: "目標配置清單(各項權重百分比之和不得超過 100%)",
				Items: &jsonschema.Schema{
					Type:     "object",
					Required: []string{"target_key", "target_percent"},
					Properties: map[string]*jsonschema.Schema{
						"target_key": {
							Type:        "string",
							Description: "標的鍵值: 股票代號(個股/ETF穿透)或產業名稱(產業)",
						},
						"target_percent": {
							Type:        "number",
							Minimum:     ptr(0.0),
							Maximum:     ptr(100.0),
							Description: "目標配置百分比 (0.0 ~ 100.0)",
						},
					},
				},
			},
			"member_id": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Default:     []byte("0"),
				Description: "會員編號; 0(預設)為所有部位",
			},
		},
	}
}

func getPortfolioTargetsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"dimension": {
				Type:        "string",
				Enum:        []any{"stock", "industry", "etf_look_through"},
				Description: "再平衡維度(若未指定則回傳全部維度設定)",
			},
			"member_id": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Default:     []byte("0"),
				Description: "會員編號; 0(預設)為所有部位",
			},
		},
	}
}

type portfolioRebalanceToolset struct {
	pr   PortfolioRebalanceQuerier
	logf func(string, ...any)
}

func (ts *portfolioRebalanceToolset) getPortfolioRebalance(ctx context.Context, _ *mcp.CallToolRequest, in RebalanceInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	dim := in.Dimension
	if dim == "" {
		dim = "stock"
	}
	if dim != "stock" && dim != "industry" && dim != "etf_look_through" {
		return nil, nil, fmt.Errorf("參數 dimension 必須為 stock, industry, 或 etf_look_through")
	}
	lotSize := in.LotSize
	if lotSize == 0 {
		lotSize = 1000
	}
	if lotSize != 1 && lotSize != 1000 {
		return nil, nil, fmt.Errorf("參數 lot_size 必須為 1 或 1000")
	}
	tolerance := in.TolerancePercent
	if tolerance == 0 {
		tolerance = 2.0
	}
	if tolerance < 0 {
		return nil, nil, fmt.Errorf("參數 tolerance_percent 不可為負數")
	}
	if in.AvailableCash < 0 {
		return nil, nil, fmt.Errorf("參數 available_cash 不可為負數")
	}
	feeDiscount := in.FeeDiscount
	if feeDiscount == 0 {
		feeDiscount = 1.0
	}
	if feeDiscount < 0 || feeDiscount > 1.0 {
		return nil, nil, fmt.Errorf("參數 fee_discount 必須介於 0.0 至 1.0")
	}

	opt := RebalanceOptions{
		MemberID:         in.MemberID,
		Dimension:        dim,
		LotSize:          lotSize,
		TolerancePercent: tolerance,
		AvailableCash:    in.AvailableCash,
		FeeDiscount:      feeDiscount,
	}

	result, err := ts.pr.PortfolioRebalance(ctx, opt)
	if err != nil {
		ts.logf("工具 get_portfolio_rebalance 執行失敗:%v", err)
		return nil, nil, err
	}

	text := formatRebalanceText(result)
	return textResult(text), PortfolioRebalanceOutput{
		DataKind:           "portfolio_rebalance",
		Disclaimer:         RebalanceDisclaimer,
		PortfolioRebalance: result,
	}, nil
}

func (ts *portfolioRebalanceToolset) setPortfolioTargets(ctx context.Context, _ *mcp.CallToolRequest, in SetTargetsInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	dim := in.Dimension
	if dim != "stock" && dim != "industry" && dim != "etf_look_through" {
		return nil, nil, fmt.Errorf("參數 dimension 必須為 stock, industry, 或 etf_look_through")
	}
	if len(in.Targets) == 0 {
		return nil, nil, fmt.Errorf("targets 清單不可為空")
	}
	var sum float64
	for _, t := range in.Targets {
		if t.TargetKey == "" {
			return nil, nil, fmt.Errorf("target_key 不可為空字串")
		}
		if t.TargetPercent < 0 || t.TargetPercent > 100.0 {
			return nil, nil, fmt.Errorf("目標比例 (%s: %.2f%%) 必須介於 0%% 至 100%%", t.TargetKey, t.TargetPercent)
		}
		sum += t.TargetPercent
	}
	if sum > 100.0001 {
		return nil, nil, fmt.Errorf("目標配置比例總和 (%.2f%%) 不得超過 100%%", sum)
	}

	opt := SetTargetsOptions{
		MemberID:  in.MemberID,
		Dimension: dim,
		Targets:   in.Targets,
	}

	result, err := ts.pr.SetPortfolioTargets(ctx, opt)
	if err != nil {
		ts.logf("工具 set_portfolio_targets 執行失敗:%v", err)
		return nil, nil, err
	}

	text := formatTargetsText(result, true)
	return textResult(text), PortfolioTargetsOutput{
		DataKind:         "portfolio_targets",
		Disclaimer:       TargetDisclaimer,
		PortfolioTargets: result,
	}, nil
}

func (ts *portfolioRebalanceToolset) getPortfolioTargets(ctx context.Context, _ *mcp.CallToolRequest, in GetTargetsInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	dim := in.Dimension
	if dim != "" && dim != "stock" && dim != "industry" && dim != "etf_look_through" {
		return nil, nil, fmt.Errorf("參數 dimension 必須為 stock, industry, 或 etf_look_through")
	}

	result, err := ts.pr.GetPortfolioTargets(ctx, in.MemberID, dim)
	if err != nil {
		ts.logf("工具 get_portfolio_targets 執行失敗:%v", err)
		return nil, nil, err
	}

	text := formatTargetsText(result, false)
	return textResult(text), PortfolioTargetsOutput{
		DataKind:         "portfolio_targets",
		Disclaimer:       TargetDisclaimer,
		PortfolioTargets: result,
	}, nil
}

func formatRebalanceText(r *PortfolioRebalance) string {
	var b summaryBuilder
	dimName := "個股"
	switch r.Dimension {
	case "industry":
		dimName = "產業"
	case "etf_look_through":
		dimName = "ETF穿透"
	}

	unitName := "整張 (1000股)"
	if r.LotSize == 1 {
		unitName = "零股 (1股)"
	}

	b.printf("持股再平衡試算建議（維度: %s | 單位: %s | 容忍帶: ±%.1f%%）", dimName, unitName, r.TolerancePercent)
	b.printf("\n投資組合總資產: %s（股票現值 %s ＋ 可用現金 %s）",
		FormatCurrencyWithUnit(&r.TotalPortfolioValue, CurrencyYuan),
		FormatCurrencyWithUnit(&r.TotalEquityValue, CurrencyYuan),
		FormatCurrencyWithUnit(&r.CashSummary.InitialCash, CurrencyYuan))
	b.printf("\n目標現金/保留比例: %.2f%%", r.TargetCashPercent)

	switch r.Dimension {
	case "stock":
		b.printf("\n\n【個股調整建議】:")
		if len(r.StockItems) == 0 {
			b.printf("\n目前無持股且未設定目標標的。")
		} else {
			for _, item := range r.StockItems {
				actionZh := "維持"
				switch item.Action {
				case "buy":
					actionZh = "買入"
				case "sell":
					actionZh = "賣出"
				}
				b.printf("\n- [%s] %s %s: 目前持股 %d 股 (現值 %s, 權重 %.2f%%) → 目標權重 %.2f%% (偏離 %+.2f%%)",
					actionZh, item.Symbol, item.Name, item.CurrentShares,
					FormatCurrencyWithUnit(&item.CurrentValue, CurrencyYuan),
					item.CurrentWeight, item.TargetWeight, item.DeviationPercent)
				if item.Action != "hold" {
					b.printf(" | 建議調整 %d 股, 預估金額 %s (手續費/稅約 %s, 淨額 %s)",
						item.SuggestedShares,
						FormatCurrencyWithUnit(&item.SuggestedAmount, CurrencyYuan),
						FormatCurrencyWithUnit(&item.EstimatedCost, CurrencyYuan),
						FormatCurrencyWithUnit(&item.NetAmount, CurrencyYuan))
				}
			}
		}

	case "industry":
		b.printf("\n\n【產業配置偏離】:")
		for _, item := range r.IndustryItems {
			cands := "無現有持股"
			if len(item.CandidateStocks) > 0 {
				cands = strings.Join(item.CandidateStocks, "、")
			}
			b.printf("\n- %s: 現值 %s (權重 %.2f%%) → 目標權重 %.2f%% (偏離 %+.2f%%) | 候選標的: %s",
				item.Industry,
				FormatCurrencyWithUnit(&item.CurrentValue, CurrencyYuan),
				item.CurrentWeight, item.TargetWeight, item.DeviationPercent, cands)
		}

	case "etf_look_through":
		b.printf("\n\n【ETF穿透曝險與偏離】:")
		for _, item := range r.EtfItems {
			b.printf("\n- %s %s: 直接持股 %s ＋ ETF間接持股 %s ＝ 合計現值 %s (穿透權重 %.2f%%) → 目標權重 %.2f%% (偏離 %+.2f%%)",
				item.StockSymbol, item.Name,
				FormatCurrencyWithUnit(&item.DirectValue, CurrencyYuan),
				FormatCurrencyWithUnit(&item.IndirectValue, CurrencyYuan),
				FormatCurrencyWithUnit(&item.TotalValue, CurrencyYuan),
				item.CurrentWeight, item.TargetWeight, item.DeviationPercent)
		}
	}

	b.printf("\n\n【現金流試算摘要】:")
	b.printf("\n- 期初可用現金: %s", FormatCurrencyWithUnit(&r.CashSummary.InitialCash, CurrencyYuan))
	b.printf("\n- 預估賣出實收淨額: %s", FormatCurrencyWithUnit(&r.CashSummary.EstimatedSellNet, CurrencyYuan))
	b.printf("\n- 可用資金總額: %s", FormatCurrencyWithUnit(&r.CashSummary.AvailableCapital, CurrencyYuan))
	b.printf("\n- 預估買入總支出: %s", FormatCurrencyWithUnit(&r.CashSummary.EstimatedBuyTotal, CurrencyYuan))
	if r.CashSummary.RequiredAdditionalCash > 0 {
		b.printf("\n- 需額外新增資金: %s（可用資本不足以執行建議買入）", FormatCurrencyWithUnit(&r.CashSummary.RequiredAdditionalCash, CurrencyYuan))
	} else {
		b.printf("\n- 試算後預估剩餘現金: %s", FormatCurrencyWithUnit(&r.CashSummary.RemainingCash, CurrencyYuan))
	}

	b.printf("\n\n免責聲明: %s", RebalanceDisclaimer)
	return b.String()
}

func formatTargetsText(t *PortfolioTargets, isSet bool) string {
	var b summaryBuilder
	actionText := "目前目標配置設定"
	if isSet {
		actionText = "目標配置設定完成"
	}

	b.printf("%s（維度: %s | 會員: %d）:", actionText, t.Dimension, t.MemberID)
	if len(t.Targets) == 0 {
		b.printf("\n目前尚未設定任何標的目標比例。")
	} else {
		for _, item := range t.Targets {
			b.printf("\n- %s: %.2f%%", item.TargetKey, item.TargetPercent)
		}
	}
	b.printf("\n目標現金/未分配比例: %.2f%%", t.TargetCashPercent)
	b.printf("\n\n免責聲明: %s", TargetDisclaimer)
	return b.String()
}
