// Package stock 的 get_industry_strength 工具:各產業成分股相對強度(RS)的分布,看資金輪動到哪些產業。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/industry-strength:個股分數以上市櫃全體計算
// (與 get_relative_strength_ranking 相同),產業強度＝成分股 RS 中位數,另附強弱勢占比、領先股,
// 以及與約 20 個交易日前的中位數比較。
package stock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// IndustryLeader 是產業內相對強度最高的個股。
type IndustryLeader struct {
	StockSymbol string `json:"stock_symbol"`
	Name        string `json:"name"`
	RSScore     int    `json:"rs_score"`
}

// IndustryStrengthItem 是單一產業的相對強度;前期比較在當時沒有計分股票時為 nil。
type IndustryStrengthItem struct {
	Rank               int              `json:"rank"`
	Industry           string           `json:"industry"`
	Stocks             int              `json:"stocks"`
	MedianRSScore      float64          `json:"median_rs_score"`
	StrongRatio        float64          `json:"strong_ratio"`
	WeakRatio          float64          `json:"weak_ratio"`
	AvgWeightedReturn  float64          `json:"avg_weighted_return"`
	PriorMedianRSScore *float64         `json:"prior_median_rs_score"`
	MedianChange       *float64         `json:"median_change"`
	Leaders            []IndustryLeader `json:"leaders"`
}

// IndustryStrengthEnvelope 是 industry-strength endpoint 的完整回應。
type IndustryStrengthEnvelope struct {
	DataAsOf  string                 `json:"data_as_of"`
	PriorAsOf *string                `json:"prior_as_of"`
	Market    string                 `json:"market"`
	Order     string                 `json:"order"`
	Universe  int                    `json:"universe"`
	Items     []IndustryStrengthItem `json:"items"`
}

// IndustryStrengthOptions 是產業相對強度的查詢條件;Date 空字串代表最新。
type IndustryStrengthOptions struct {
	Date      string
	Market    string
	MinStocks int
	Order     string
}

// IndustryStrengthQuerier 是 get_industry_strength 對資料來源的需求介面;只有 *APIClient 實作。
type IndustryStrengthQuerier interface {
	IndustryStrength(ctx context.Context, opt IndustryStrengthOptions) (*IndustryStrengthEnvelope, error)
}

// IndustryStrength 查詢產業相對強度;404 代表指定日期之前查無相對強度資料
// (日期、市場、排序與檔數門檻在工具層已驗證)。
func (c *APIClient) IndustryStrength(ctx context.Context, opt IndustryStrengthOptions) (*IndustryStrengthEnvelope, error) {
	values := url.Values{
		"market":     []string{opt.Market},
		"min_stocks": []string{fmt.Sprint(opt.MinStocks)},
		"order":      []string{opt.Order},
	}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	var body IndustryStrengthEnvelope
	found, err := c.getFound(ctx, "/api/v1/market/industry-strength?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	return &body, nil
}

// IndustryStrengthInput 是 get_industry_strength 的輸入。
type IndustryStrengthInput struct {
	Date      string `json:"date,omitempty"`
	Market    string `json:"market,omitempty"`
	MinStocks int    `json:"min_stocks,omitempty"`
	Order     string `json:"order,omitempty"`
}

// IndustryStrengthOutput 是產業相對強度 envelope 加上 MCP 分析中繼資料。
type IndustryStrengthOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*IndustryStrengthEnvelope
}

// industryStrengthSchema 描述 get_industry_strength 的輸入形狀。
func industryStrengthSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "交易日(格式 YYYY-MM-DD,選填);未提供為最新一個已算好的交易日",
			},
			"market": listedOTCMarketSchema(),
			"min_stocks": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Default:     []byte("3"),
				Description: "產業至少要有幾檔計分股票才列出(預設 3,範圍 1 至 50;檔數太少的產業中位數不穩定)",
			},
			"order": {
				Type:        "string",
				Enum:        []any{"desc", "asc"},
				Default:     []byte(`"desc"`),
				Description: "desc(預設,最強產業在前)或 asc(最弱產業在前),依 RS 中位數排序",
			},
		},
	}
}

// industryStrengthToolset 綁定產業相對強度資料來源與記錄函式。
type industryStrengthToolset struct {
	is   IndustryStrengthQuerier
	logf func(string, ...any)
}

// industryStrength 執行 get_industry_strength。
func (ts *industryStrengthToolset) industryStrength(ctx context.Context, _ *mcp.CallToolRequest, in IndustryStrengthInput) (*mcp.CallToolResult, any, error) {
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	minStocks, err := rangedLimit(in.MinStocks, 3, 1, 50)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 min_stocks 必須介於 1 到 50 之間")
	}
	order := strings.ToLower(strings.TrimSpace(in.Order))
	if order == "" {
		order = "desc"
	}
	if order != "desc" && order != "asc" {
		return nil, nil, fmt.Errorf("參數 order 必須為 desc 或 asc,收到了 %q", in.Order)
	}
	envelope, err := ts.is.IndustryStrength(ctx, IndustryStrengthOptions{Date: date, Market: market, MinStocks: minStocks, Order: order})
	if err != nil {
		if errors.Is(err, ErrMarketDataNotFound) {
			return nil, nil, fmt.Errorf("指定日期之前查無相對強度資料")
		}
		ts.logf("工具 get_industry_strength 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(industryStrengthSummary(envelope)), IndustryStrengthOutput{
		DataKind: "industry_strength", IsRealtime: false, Disclaimer: AnalysisDisclaimer, IndustryStrengthEnvelope: envelope,
	}, nil
}

// industryStrengthSummary 組出給模型閱讀的文字摘要:計分方式、各產業中位數、占比、變化與領先股。
func industryStrengthSummary(e *IndustryStrengthEnvelope) string {
	var b summaryBuilder
	direction := "由強到弱"
	if e.Order == "asc" {
		direction = "由弱到強"
	}
	b.printf("%s 產業相對強度(%s 市場,%s):產業強度＝成分股 RS 中位數,個股 RS 1–99 為加權報酬"+
		"(0.4×近 60 日+0.3×近 120 日+0.3×近 240 日,還原權值含息)在上市櫃全體 %d 檔中的百分位;變化與 %s 比較。",
		e.DataAsOf, e.Market, direction, e.Universe, displayString(e.PriorAsOf))
	if len(e.Items) == 0 {
		b.printf("\n沒有符合檔數門檻的產業。")
	}
	for _, item := range e.Items {
		leaders := make([]string, 0, len(item.Leaders))
		for _, leader := range item.Leaders {
			leaders = append(leaders, fmt.Sprintf("%s %s %d", leader.StockSymbol, leader.Name, leader.RSScore))
		}
		b.printf("\n- 第 %d 名 %s(%d 檔):RS 中位數 %s、變化 %s;強勢(≥80)%s、弱勢(≤20)%s;平均加權報酬 %s%%;領先:%s",
			item.Rank, item.Industry, item.Stocks, formatFloat(item.MedianRSScore), signedFloat(item.MedianChange),
			ratioPercent(item.StrongRatio), ratioPercent(item.WeakRatio), formatFloat(item.AvgWeightedReturn), strings.Join(leaders, "、"))
	}
	b.printf("\n過去表現不代表未來結果。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// signedFloat 把數值加上正負號;nil 回「無」。
func signedFloat(v *float64) string {
	if v == nil {
		return "無"
	}
	if *v > 0 {
		return "+" + formatFloat(*v)
	}
	return formatFloat(*v)
}

// ratioPercent 把 0–1 的比例轉成百分比文字,四捨五入到小數一位。
func ratioPercent(ratio float64) string {
	return formatFloat(math.Round(ratio*1000)/10) + "%"
}
