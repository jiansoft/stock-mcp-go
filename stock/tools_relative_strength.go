// Package stock 的 get_relative_strength_ranking 工具:上市櫃股票的相對強度(RS)排行。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/relative-strength:加權報酬
// (0.4 × 近 60 日 + 0.3 × 近 120 日 + 0.3 × 近 240 日,還原權值、含息)在上市櫃普通股(不含 ETF、特別股)中的
// 百分位分數 1–99 與名次。市場、產業、代號只篩選輸出,分數與名次一律以全體計算。
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

// RelativeStrengthItem 是相對強度排行中的單一股票;缺值時為 nil。
type RelativeStrengthItem struct {
	Rank           int      `json:"rank"`
	StockSymbol    string   `json:"stock_symbol"`
	Name           string   `json:"name"`
	Market         string   `json:"market"`
	Industry       string   `json:"industry"`
	RSScore        int      `json:"rs_score"`
	WeightedReturn float64  `json:"weighted_return"`
	Return20d      *float64 `json:"return_20d"`
	Return60d      float64  `json:"return_60d"`
	Return120d     float64  `json:"return_120d"`
	Return240d     float64  `json:"return_240d"`
	ClosingPrice   *float64 `json:"closing_price"`
}

// RelativeStrengthEnvelope 是 relative-strength endpoint 的完整回應。
type RelativeStrengthEnvelope struct {
	DataAsOf string                 `json:"data_as_of"`
	Market   string                 `json:"market"`
	Order    string                 `json:"order"`
	Universe int                    `json:"universe"`
	Matched  int                    `json:"matched"`
	Items    []RelativeStrengthItem `json:"items"`
}

// RelativeStrengthOptions 是相對強度排行的查詢條件;空字串代表不篩選或使用預設。
type RelativeStrengthOptions struct {
	Date     string
	Market   string
	Industry string
	Symbol   string
	Order    string
	Limit    int
}

// RelativeStrengthQuerier 是 get_relative_strength_ranking 對資料來源的需求介面;只有 *APIClient 實作。
type RelativeStrengthQuerier interface {
	RelativeStrength(ctx context.Context, opt RelativeStrengthOptions) (*RelativeStrengthEnvelope, error)
}

// RelativeStrength 查詢相對強度排行;404 代表代號不存在或查無資料,422 代表產業名稱不存在
// (日期、市場、排序與筆數在工具層已驗證)。
func (c *APIClient) RelativeStrength(ctx context.Context, opt RelativeStrengthOptions) (*RelativeStrengthEnvelope, error) {
	values := url.Values{
		"market": []string{opt.Market},
		"order":  []string{opt.Order},
		"limit":  []string{fmt.Sprint(opt.Limit)},
	}
	for key, value := range map[string]string{"date": opt.Date, "industry": opt.Industry, "symbol": opt.Symbol} {
		if value != "" {
			values.Set(key, value)
		}
	}
	var body RelativeStrengthEnvelope
	found, err := c.getFound(ctx, "/api/v1/market/relative-strength?"+values.Encode(), &body)
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

// RelativeStrengthInput 是 get_relative_strength_ranking 的輸入。
type RelativeStrengthInput struct {
	Date     string `json:"date,omitempty"`
	Market   string `json:"market,omitempty"`
	Industry string `json:"industry,omitempty"`
	Symbol   string `json:"symbol,omitempty"`
	Order    string `json:"order,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// RelativeStrengthOutput 是相對強度 envelope 加上 MCP 分析中繼資料。
type RelativeStrengthOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*RelativeStrengthEnvelope
}

// relativeStrengthSchema 描述 get_relative_strength_ranking 的輸入形狀。
func relativeStrengthSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "交易日(格式 YYYY-MM-DD,選填);未提供為最新一個已算好的交易日",
			},
			"market": listedOTCMarketSchema(),
			"industry": {
				Type:        "string",
				MaxLength:   ptr(20),
				Description: "只列出這個產業(選填),需為完整產業名稱,例如「半導體業」;分數與名次仍以上市櫃普通股(不含 ETF、特別股)計算",
			},
			"symbol": {
				Type:        "string",
				MinLength:   ptr(1),
				MaxLength:   ptr(24),
				Description: "只列出這檔股票(選填),用來查單一股票的相對強度分數與名次",
			},
			"order": {
				Type:        "string",
				Enum:        []any{"desc", "asc"},
				Default:     []byte(`"desc"`),
				Description: "desc(預設,最強在前)或 asc(最弱在前)",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(200.0),
				Default:     []byte("30"),
				Description: "最多列出幾筆(預設 30,範圍 1 至 200)",
			},
		},
	}
}

// relativeStrengthToolset 綁定相對強度資料來源與記錄函式。
type relativeStrengthToolset struct {
	rs   RelativeStrengthQuerier
	logf func(string, ...any)
}

// relativeStrength 執行 get_relative_strength_ranking。
func (ts *relativeStrengthToolset) relativeStrength(ctx context.Context, _ *mcp.CallToolRequest, in RelativeStrengthInput) (*mcp.CallToolResult, any, error) {
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	industry := strings.TrimSpace(in.Industry)
	if err := validateLength(industry, "industry", 0, 20); err != nil {
		return nil, nil, err
	}
	symbol := ""
	if strings.TrimSpace(in.Symbol) != "" {
		if symbol, err = normalizeSymbol(in.Symbol); err != nil {
			return nil, nil, err
		}
	}
	order := strings.ToLower(strings.TrimSpace(in.Order))
	if order == "" {
		order = "desc"
	}
	if order != "desc" && order != "asc" {
		return nil, nil, fmt.Errorf("參數 order 必須為 desc 或 asc,收到了 %q", in.Order)
	}
	limit, err := rangedLimit(in.Limit, 30, 1, 200)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 limit 必須介於 1 到 200 之間")
	}
	envelope, err := ts.rs.RelativeStrength(ctx, RelativeStrengthOptions{
		Date: date, Market: market, Industry: industry, Symbol: symbol, Order: order, Limit: limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrMarketDataNotFound):
			if symbol != "" {
				return nil, nil, fmt.Errorf("找不到股票代號 %s,或指定日期之前查無相對強度資料", symbol)
			}
			return nil, nil, fmt.Errorf("指定日期之前查無相對強度資料")
		case errors.Is(err, ErrIndustryNotFound):
			return nil, nil, fmt.Errorf("找不到產業「%s」,請使用完整產業名稱,例如「半導體業」", industry)
		}
		ts.logf("工具 get_relative_strength_ranking 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(relativeStrengthSummary(envelope, symbol)), RelativeStrengthOutput{
		DataKind: "relative_strength", IsRealtime: false, Disclaimer: AnalysisDisclaimer, RelativeStrengthEnvelope: envelope,
	}, nil
}

// relativeStrengthSummary 組出給模型閱讀的文字摘要:計分方式、篩選結果與各股分數。
func relativeStrengthSummary(e *RelativeStrengthEnvelope, symbol string) string {
	var b summaryBuilder
	direction := "由強到弱"
	if e.Order == "asc" {
		direction = "由弱到強"
	}
	b.printf("%s 相對強度排行(%s 市場,%s):分數 1–99 為加權報酬(0.4×近 60 日+0.3×近 120 日+0.3×近 240 日,還原權值含息)"+
		"在上市櫃普通股(不含 ETF、特別股)%d 檔中的百分位,99 最強;符合條件 %d 檔。",
		e.DataAsOf, e.Market, direction, e.Universe, e.Matched)
	if len(e.Items) == 0 {
		if symbol != "" {
			b.printf("\n%s 未計分(上市未滿 240 個交易日、暫停交易或非上市櫃)。", symbol)
		} else {
			b.printf("\n沒有符合條件的股票。")
		}
	}
	for _, item := range e.Items {
		b.printf("\n- 第 %d 名 %s %s(%s,%s):RS %d、加權 %s%%;近 20 日 %s、60 日 %s%%、120 日 %s%%、240 日 %s%%;收盤 %s 元",
			item.Rank, item.StockSymbol, item.Name, item.Market, item.Industry, item.RSScore, formatFloat(item.WeightedReturn),
			displayPercent(item.Return20d), formatFloat(item.Return60d), formatFloat(item.Return120d), formatFloat(item.Return240d),
			displayFloat(item.ClosingPrice))
	}
	if rest := e.Matched - len(e.Items); rest > 0 {
		b.printf("\n另有 %d 檔未列出,可提高 limit 或加上 market、industry 篩選。", rest)
	}
	b.printf("\n過去表現不代表未來結果。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
