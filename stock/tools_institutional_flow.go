// Package stock 的 get_institutional_flow_ranking 工具:三大法人近 N 個交易日的買賣超排行。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/institutional-flow:讀每晚 21:50 入庫的全市場
// 三大法人買賣超,累計淨股數並以每日淨股數×當日收盤價估算金額,依金額排名。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// InstitutionalFlowItem 是法人買賣超排行的一檔股票;占比與收盤價可能為 nil。
type InstitutionalFlowItem struct {
	Rank         int      `json:"rank"`
	StockSymbol  string   `json:"stock_symbol"`
	Name         string   `json:"name"`
	Market       string   `json:"market"`
	Industry     string   `json:"industry"`
	NetShares    int64    `json:"net_shares"`
	NetLots      int64    `json:"net_lots"`
	NetAmount    float64  `json:"net_amount"`
	VolumeRatio  *float64 `json:"volume_ratio"`
	BuyDays      int      `json:"buy_days"`
	SellDays     int      `json:"sell_days"`
	Streak       int64    `json:"streak"`
	ClosingPrice *float64 `json:"closing_price"`
}

// InstitutionalFlowEnvelope 是 institutional-flow endpoint 的完整回應。
type InstitutionalFlowEnvelope struct {
	DataAsOf string                  `json:"data_as_of"`
	From     string                  `json:"from"`
	Days     int                     `json:"days"`
	Investor string                  `json:"investor"`
	Side     string                  `json:"side"`
	Market   string                  `json:"market"`
	Industry *string                 `json:"industry"`
	Matched  int                     `json:"matched"`
	Items    []InstitutionalFlowItem `json:"items"`
}

// InstitutionalFlowOptions 是法人買賣超排行的查詢條件;Date、Industry 空字串代表不指定。
type InstitutionalFlowOptions struct {
	Date     string
	Days     int
	Investor string
	Side     string
	Market   string
	Industry string
	Limit    int
}

// InstitutionalFlowQuerier 是 get_institutional_flow_ranking 對資料來源的需求介面;只有 *APIClient 實作。
type InstitutionalFlowQuerier interface {
	InstitutionalFlow(ctx context.Context, opt InstitutionalFlowOptions) (*InstitutionalFlowEnvelope, error)
}

// InstitutionalFlow 查詢法人買賣超排行;404 代表指定日期之前查無籌碼資料,422 代表產業名稱不存在
// (其餘參數在工具層已驗證)。
func (c *APIClient) InstitutionalFlow(ctx context.Context, opt InstitutionalFlowOptions) (*InstitutionalFlowEnvelope, error) {
	values := url.Values{
		"days":     []string{fmt.Sprint(opt.Days)},
		"investor": []string{opt.Investor},
		"side":     []string{opt.Side},
		"market":   []string{opt.Market},
		"limit":    []string{fmt.Sprint(opt.Limit)},
	}
	for key, value := range map[string]string{"date": opt.Date, "industry": opt.Industry} {
		if value != "" {
			values.Set(key, value)
		}
	}
	var body InstitutionalFlowEnvelope
	found, err := c.getFound(ctx, "/api/v1/market/institutional-flow?"+values.Encode(), &body)
	if err != nil {
		var status *apiStatusError
		if errors.As(err, &status) && status.status == http.StatusUnprocessableEntity {
			return nil, ErrIndustryNotFound
		}
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	return &body, nil
}

// InstitutionalFlowInput 是 get_institutional_flow_ranking 的輸入。
type InstitutionalFlowInput struct {
	Date     string `json:"date,omitempty"`
	Days     int    `json:"days,omitempty"`
	Investor string `json:"investor,omitempty"`
	Side     string `json:"side,omitempty"`
	Market   string `json:"market,omitempty"`
	Industry string `json:"industry,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// InstitutionalFlowOutput 是法人買賣超排行 envelope 加上 MCP 分析中繼資料。
type InstitutionalFlowOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*InstitutionalFlowEnvelope
}

// investorLabels 是法人別的中文名稱,也是合法值清單。
var investorLabels = map[string]string{
	"foreign": "外資",
	"trust":   "投信",
	"dealer":  "自營商",
	"total":   "三大法人合計",
}

// investorSchema 描述法人別參數。
func investorSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        []any{"foreign", "trust", "dealer", "total"},
		Default:     []byte(`"foreign"`),
		Description: "法人別:foreign(預設,外資及陸資)、trust(投信)、dealer(自營商)或 total(三大法人合計)",
	}
}

// flowDaysSchema 描述累計交易日數參數。
func flowDaysSchema(def int) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "integer",
		Minimum:     ptr(1.0),
		Maximum:     ptr(60.0),
		Default:     []byte(fmt.Sprint(def)),
		Description: fmt.Sprintf("累計最近幾個有籌碼資料的交易日(預設 %d,範圍 1 至 60)", def),
	}
}

// institutionalFlowSchema 描述 get_institutional_flow_ranking 的輸入形狀。
func institutionalFlowSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "交易日(格式 YYYY-MM-DD,選填);未提供為最新一個有籌碼資料的交易日(每晚 21:50 入庫)",
			},
			"days":     flowDaysSchema(1),
			"investor": investorSchema(),
			"side": {
				Type:        "string",
				Enum:        []any{"buy", "sell"},
				Default:     []byte(`"buy"`),
				Description: "buy(預設,買超排行)或 sell(賣超排行),依估計淨買賣金額排序",
			},
			"market": listedOTCMarketSchema(),
			"industry": {
				Type:        "string",
				MaxLength:   ptr(20),
				Description: "產業名稱(選填,完整名稱,例如「半導體業」)",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100.0),
				Default:     []byte("30"),
				Description: "最多回傳幾檔(預設 30,範圍 1 至 100)",
			},
		},
	}
}

// institutionalFlowToolset 綁定法人買賣超資料來源與記錄函式。
type institutionalFlowToolset struct {
	fq   InstitutionalFlowQuerier
	logf func(string, ...any)
}

// normalizeInvestor 整理法人別參數;空字串為 foreign。
func normalizeInvestor(raw string) (string, error) {
	investor := strings.ToLower(strings.TrimSpace(raw))
	if investor == "" {
		return "foreign", nil
	}
	if _, ok := investorLabels[investor]; !ok {
		return "", fmt.Errorf("參數 investor 必須為 foreign、trust、dealer 或 total,收到了 %q", raw)
	}
	return investor, nil
}

// institutionalFlow 執行 get_institutional_flow_ranking。
func (ts *institutionalFlowToolset) institutionalFlow(ctx context.Context, _ *mcp.CallToolRequest, in InstitutionalFlowInput) (*mcp.CallToolResult, any, error) {
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	days, err := rangedLimit(in.Days, 1, 1, 60)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 60 之間")
	}
	investor, err := normalizeInvestor(in.Investor)
	if err != nil {
		return nil, nil, err
	}
	side := strings.ToLower(strings.TrimSpace(in.Side))
	if side == "" {
		side = "buy"
	}
	if side != "buy" && side != "sell" {
		return nil, nil, fmt.Errorf("參數 side 必須為 buy 或 sell,收到了 %q", in.Side)
	}
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	industry := strings.TrimSpace(in.Industry)
	if err := validateLength(industry, "industry", 0, 20); err != nil {
		return nil, nil, err
	}
	limit, err := rangedLimit(in.Limit, 30, 1, 100)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 limit 必須介於 1 到 100 之間")
	}
	envelope, err := ts.fq.InstitutionalFlow(ctx, InstitutionalFlowOptions{
		Date: date, Days: days, Investor: investor, Side: side, Market: market, Industry: industry, Limit: limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrMarketDataNotFound):
			return nil, nil, fmt.Errorf("指定日期之前查無籌碼資料")
		case errors.Is(err, ErrIndustryNotFound):
			return nil, nil, fmt.Errorf("找不到產業「%s」,請使用完整產業名稱,例如「半導體業」", industry)
		}
		ts.logf("工具 get_institutional_flow_ranking 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(institutionalFlowSummary(envelope)), InstitutionalFlowOutput{
		DataKind: "institutional_flow", IsRealtime: false, Disclaimer: AnalysisDisclaimer, InstitutionalFlowEnvelope: envelope,
	}, nil
}

// institutionalFlowSummary 組出給模型閱讀的文字摘要:期間、估算方式與各股淨買賣、占比、連續天數。
func institutionalFlowSummary(e *InstitutionalFlowEnvelope) string {
	var b summaryBuilder
	side := "買超"
	if e.Side == "sell" {
		side = "賣超"
	}
	scope := e.Market + " 市場"
	if e.Industry != nil {
		scope += "、" + *e.Industry
	}
	b.printf("%s%s排行(%s 至 %s 共 %d 個交易日,%s):金額為每日淨股數×當日收盤價加總的估計值;符合 %d 檔。",
		investorLabels[e.Investor], side, e.From, e.DataAsOf, e.Days, scope, e.Matched)
	if len(e.Items) == 0 {
		b.printf("\n沒有符合條件的股票。")
	}
	for _, item := range e.Items {
		industry := item.Industry
		if industry == "" {
			industry = "未分類"
		}
		b.printf("\n- 第 %d 名 %s %s(%s,%s):淨%s %s 張、約 %s,占期間成交量 %s;買超 %d 天、賣超 %d 天,%s;收盤 %s",
			item.Rank, item.StockSymbol, item.Name, item.Market, industry, side, signedText(item.NetLots),
			FormatCurrencyWithUnit(&item.NetAmount, CurrencyYuan), displayPercent(item.VolumeRatio),
			item.BuyDays, item.SellDays, streakText(item.Streak), displayFloat(item.ClosingPrice))
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
