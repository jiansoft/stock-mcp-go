// Package stock 的 get_margin_ranking 工具:融資融券增減與券資比排行。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/margin-ranking:讀每晚 21:50 入庫的全市場
// 融資融券餘額(張),比較最近 N 個交易日的增減。
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

// MarginRankItem 是融資融券排行的一檔股票;單位皆為張,百分比與券資比可能為 nil。
type MarginRankItem struct {
	Rank                int      `json:"rank"`
	StockSymbol         string   `json:"stock_symbol"`
	Name                string   `json:"name"`
	Market              string   `json:"market"`
	Industry            string   `json:"industry"`
	MarginBalance       int64    `json:"margin_balance"`
	MarginChange        int64    `json:"margin_change"`
	MarginChangePercent *float64 `json:"margin_change_percent"`
	ShortBalance        int64    `json:"short_balance"`
	ShortChange         int64    `json:"short_change"`
	ShortMarginRatio    *float64 `json:"short_margin_ratio"`
	ClosingPrice        *float64 `json:"closing_price"`
}

// MarginRankingEnvelope 是 margin-ranking endpoint 的完整回應。
type MarginRankingEnvelope struct {
	DataAsOf string           `json:"data_as_of"`
	From     string           `json:"from"`
	Days     int              `json:"days"`
	RankBy   string           `json:"rank_by"`
	Market   string           `json:"market"`
	Industry *string          `json:"industry"`
	Matched  int              `json:"matched"`
	Items    []MarginRankItem `json:"items"`
}

// MarginRankingOptions 是融資融券排行的查詢條件;Date、Industry 空字串代表不指定。
type MarginRankingOptions struct {
	Date     string
	Days     int
	RankBy   string
	Market   string
	Industry string
	Limit    int
}

// MarginRankingQuerier 是 get_margin_ranking 對資料來源的需求介面;只有 *APIClient 實作。
type MarginRankingQuerier interface {
	MarginRanking(ctx context.Context, opt MarginRankingOptions) (*MarginRankingEnvelope, error)
}

// MarginRanking 查詢融資融券排行;404 代表指定日期之前查無籌碼資料,422 代表產業名稱不存在
// (其餘參數在工具層已驗證)。
func (c *APIClient) MarginRanking(ctx context.Context, opt MarginRankingOptions) (*MarginRankingEnvelope, error) {
	values := url.Values{
		"days":    []string{fmt.Sprint(opt.Days)},
		"rank_by": []string{opt.RankBy},
		"market":  []string{opt.Market},
		"limit":   []string{fmt.Sprint(opt.Limit)},
	}
	for key, value := range map[string]string{"date": opt.Date, "industry": opt.Industry} {
		if value != "" {
			values.Set(key, value)
		}
	}
	var body MarginRankingEnvelope
	found, err := c.getFound(ctx, "/api/v1/market/margin-ranking?"+values.Encode(), &body)
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

// MarginRankingInput 是 get_margin_ranking 的輸入。
type MarginRankingInput struct {
	Date     string `json:"date,omitempty"`
	Days     int    `json:"days,omitempty"`
	RankBy   string `json:"rank_by,omitempty"`
	Market   string `json:"market,omitempty"`
	Industry string `json:"industry,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// MarginRankingOutput 是融資融券排行 envelope 加上 MCP 分析中繼資料。
type MarginRankingOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*MarginRankingEnvelope
}

// marginRankLabels 是排序鍵的中文名稱,也是合法值清單。
var marginRankLabels = map[string]string{
	"margin_increase":    "融資增加",
	"margin_decrease":    "融資減少",
	"short_increase":     "融券增加",
	"short_decrease":     "融券減少",
	"short_margin_ratio": "券資比",
}

// marginRankingSchema 描述 get_margin_ranking 的輸入形狀。
func marginRankingSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "交易日(格式 YYYY-MM-DD,選填);未提供為最新一個有籌碼資料的交易日(每晚 21:50 入庫)",
			},
			"days": flowDaysSchema(1),
			"rank_by": {
				Type:    "string",
				Enum:    []any{"margin_increase", "margin_decrease", "short_increase", "short_decrease", "short_margin_ratio"},
				Default: []byte(`"margin_increase"`),
				Description: "排序鍵:margin_increase(預設,融資增加最多)、margin_decrease(融資減少最多)、" +
					"short_increase(融券增加最多)、short_decrease(融券減少最多)、short_margin_ratio(券資比最高,融資至少 100 張)",
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

// marginRankingToolset 綁定融資融券資料來源與記錄函式。
type marginRankingToolset struct {
	mq   MarginRankingQuerier
	logf func(string, ...any)
}

// marginRanking 執行 get_margin_ranking。
func (ts *marginRankingToolset) marginRanking(ctx context.Context, _ *mcp.CallToolRequest, in MarginRankingInput) (*mcp.CallToolResult, any, error) {
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	days, err := rangedLimit(in.Days, 1, 1, 60)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 60 之間")
	}
	rankBy := strings.ToLower(strings.TrimSpace(in.RankBy))
	if rankBy == "" {
		rankBy = "margin_increase"
	}
	if _, ok := marginRankLabels[rankBy]; !ok {
		return nil, nil, fmt.Errorf("參數 rank_by 必須為 margin_increase、margin_decrease、short_increase、short_decrease 或 short_margin_ratio,收到了 %q", in.RankBy)
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
	envelope, err := ts.mq.MarginRanking(ctx, MarginRankingOptions{
		Date: date, Days: days, RankBy: rankBy, Market: market, Industry: industry, Limit: limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrMarketDataNotFound):
			return nil, nil, fmt.Errorf("指定日期之前查無籌碼資料")
		case errors.Is(err, ErrIndustryNotFound):
			return nil, nil, fmt.Errorf("找不到產業「%s」,請使用完整產業名稱,例如「半導體業」", industry)
		}
		ts.logf("工具 get_margin_ranking 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(marginRankingSummary(envelope)), MarginRankingOutput{
		DataKind: "margin_ranking", IsRealtime: false, Disclaimer: AnalysisDisclaimer, MarginRankingEnvelope: envelope,
	}, nil
}

// marginRankingSummary 組出給模型閱讀的文字摘要:期間、排序方式與各股餘額、增減、券資比。
func marginRankingSummary(e *MarginRankingEnvelope) string {
	var b summaryBuilder
	scope := e.Market + " 市場"
	if e.Industry != nil {
		scope += "、" + *e.Industry
	}
	b.printf("%s排行(%s 至 %s 共 %d 個交易日,%s):增減＝資料日餘額減期間第一天的前日餘額,單位為張;符合 %d 檔。",
		marginRankLabels[e.RankBy], e.From, e.DataAsOf, e.Days, scope, e.Matched)
	if len(e.Items) == 0 {
		b.printf("\n沒有符合條件的股票。")
	}
	for _, item := range e.Items {
		industry := item.Industry
		if industry == "" {
			industry = "未分類"
		}
		b.printf("\n- 第 %d 名 %s %s(%s,%s):融資 %d 張(%s 張,%s)、融券 %d 張(%s 張)、券資比 %s;收盤 %s",
			item.Rank, item.StockSymbol, item.Name, item.Market, industry,
			item.MarginBalance, signedText(item.MarginChange), displayPercent(item.MarginChangePercent),
			item.ShortBalance, signedText(item.ShortChange), displayPercent(item.ShortMarginRatio), displayFloat(item.ClosingPrice))
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
