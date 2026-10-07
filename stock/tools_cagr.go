package stock

// get_cagr_ranking 與 get_stock_cagr:查詢 stock_rust 每日計算的各期間年化報酬率(CAGR)。
//
// 資料來源是 Data API 的 GET /api/v1/market/cagr-ranking 與
// GET /api/v1/market/cagr-ranking/{stock_symbol}。報酬以固定投入金額回測,
// metric 決定報酬口徑:price(只看股價)、total(含現金股利)、reinvested(股利再投入)。
// 金額與百分比沿用 Data API 的十進位字串,不轉成浮點數,避免精度誤差。

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// cagrPeriods 是 Data API 支援的統計期間,依長度由短到長。
var cagrPeriods = []string{"M3", "M6", "Y1", "Y2", "Y3", "Y5", "Y7", "Y10"}

// cagrMetrics 是報酬口徑。
var cagrMetrics = []string{"price", "total", "reinvested"}

// CagrRankingItem 是排行中的一檔股票;資料不足時 rank 與報酬欄位為 nil。
type CagrRankingItem struct {
	Rank            *int64  `json:"rank"`
	StockSymbol     string  `json:"stock_symbol"`
	Name            string  `json:"name"`
	StockIndustryID int     `json:"stock_industry_id"`
	BaseDate        *string `json:"base_date"`
	FirstQuoteDate  *string `json:"first_quote_date"`
	ShortfallDays   *int    `json:"shortfall_days"`
	BasePrice       *string `json:"base_price"`
	EndPrice        *string `json:"end_price"`
	EndShares       *string `json:"end_shares"`
	CashReceived    *string `json:"cash_received"`
	EndValue        *string `json:"end_value"`
	TotalReturnPct  *string `json:"total_return_pct"`
	CagrPct         *string `json:"cagr_pct"`
	DividendEvents  int     `json:"dividend_events"`
	DataComplete    bool    `json:"data_complete"`
	HasAnomaly      bool    `json:"has_anomaly"`
}

// CagrCoverage 是樣本涵蓋統計(不受篩選影響)。
type CagrCoverage struct {
	Universe         int64  `json:"universe"`
	Counted          int64  `json:"counted"`
	CoverageRatio    string `json:"coverage_ratio"`
	Incomplete       int64  `json:"incomplete"`
	AnomalyFlagged   int64  `json:"anomaly_flagged"`
	SurvivorshipNote bool   `json:"survivorship_note"`
}

// CagrSummary 是正報酬摘要。
type CagrSummary struct {
	Positive      int64  `json:"positive"`
	PositiveRatio string `json:"positive_ratio"`
}

// CagrRanking 是 cagr-ranking endpoint 的完整回應。
type CagrRanking struct {
	Period    string            `json:"period"`
	Metric    string            `json:"metric"`
	Sort      string            `json:"sort"`
	Date      string            `json:"date"`
	BaseDate  *string           `json:"base_date"`
	Years     *string           `json:"years"`
	Principal int64             `json:"principal"`
	Total     int64             `json:"total"`
	Coverage  CagrCoverage      `json:"coverage"`
	Summary   CagrSummary       `json:"summary"`
	Items     []CagrRankingItem `json:"items"`
}

// CagrPeriodItem 是單一股票在單一期間的回測結果。
type CagrPeriodItem struct {
	Period           string  `json:"period"`
	BaseDate         *string `json:"base_date"`
	FirstQuoteDate   *string `json:"first_quote_date"`
	ShortfallDays    *int    `json:"shortfall_days"`
	BasePrice        *string `json:"base_price"`
	EndPrice         *string `json:"end_price"`
	EndShares        *string `json:"end_shares"`
	CashReceived     *string `json:"cash_received"`
	EndValue         *string `json:"end_value"`
	TotalReturnPct   *string `json:"total_return_pct"`
	CagrPct          *string `json:"cagr_pct"`
	Years            *string `json:"years"`
	DividendEvents   int     `json:"dividend_events"`
	DataComplete     bool    `json:"data_complete"`
	HasAnomaly       bool    `json:"has_anomaly"`
	SurvivorshipNote bool    `json:"survivorship_note"`
}

// StockCagr 是個股全期間 CAGR 的完整回應。
type StockCagr struct {
	StockSymbol string           `json:"stock_symbol"`
	Name        *string          `json:"name"`
	Metric      string           `json:"metric"`
	Date        string           `json:"date"`
	Principal   int64            `json:"principal"`
	Items       []CagrPeriodItem `json:"items"`
}

// CagrRankingOptions 是 CAGR 排行的查詢條件;空字串與 0 代表採用 Data API 預設。
type CagrRankingOptions struct {
	Period            string
	Metric            string
	Sort              string
	Market            string
	IndustryID        int
	IncludeIncomplete bool
	Limit             int
	Offset            int
}

// CagrQuerier 是兩個 CAGR 工具對資料來源的需求介面。
type CagrQuerier interface {
	CagrRanking(ctx context.Context, opt CagrRankingOptions) (*CagrRanking, error)
	StockCagr(ctx context.Context, symbol, metric string) (*StockCagr, error)
}

// CagrRanking 查詢 CAGR 排行;404 代表尚無計算結果。
func (c *APIClient) CagrRanking(ctx context.Context, opt CagrRankingOptions) (*CagrRanking, error) {
	values := url.Values{
		"period":             []string{opt.Period},
		"metric":             []string{opt.Metric},
		"market":             []string{opt.Market},
		"include_incomplete": []string{fmt.Sprint(opt.IncludeIncomplete)},
		"limit":              []string{fmt.Sprint(opt.Limit)},
		"offset":             []string{fmt.Sprint(opt.Offset)},
	}
	if opt.Sort != "" {
		values.Set("sort", opt.Sort)
	}
	if opt.IndustryID > 0 {
		values.Set("stock_industry_id", fmt.Sprint(opt.IndustryID))
	}
	var body CagrRanking
	found, err := c.getFound(ctx, "/api/v1/market/cagr-ranking?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	if body.Items == nil {
		body.Items = []CagrRankingItem{}
	}
	return &body, nil
}

// StockCagr 查詢個股全部期間的 CAGR;404 代表代號不存在或尚無計算結果。
func (c *APIClient) StockCagr(ctx context.Context, symbol, metric string) (*StockCagr, error) {
	path := "/api/v1/market/cagr-ranking/" + url.PathEscape(symbol) + "?" + url.Values{"metric": []string{metric}}.Encode()
	var body StockCagr
	found, err := c.getFound(ctx, path, &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	if body.Items == nil {
		body.Items = []CagrPeriodItem{}
	}
	return &body, nil
}

// CagrRankingInput 是 get_cagr_ranking 的輸入。
type CagrRankingInput struct {
	Period            string `json:"period,omitempty"`
	Metric            string `json:"metric,omitempty"`
	Sort              string `json:"sort,omitempty"`
	Market            string `json:"market,omitempty"`
	IndustryID        int    `json:"industry_id,omitempty"`
	IncludeIncomplete bool   `json:"include_incomplete,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	Offset            int    `json:"offset,omitempty"`
}

// StockCagrInput 是 get_stock_cagr 的輸入。
type StockCagrInput struct {
	Symbol string `json:"symbol"`
	Metric string `json:"metric,omitempty"`
}

// CagrRankingOutput 是排行 envelope 加上 MCP 分析中繼資料。
type CagrRankingOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*CagrRanking
}

// StockCagrOutput 是個股 envelope 加上 MCP 分析中繼資料。
type StockCagrOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*StockCagr
}

func cagrMetricSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        []any{"price", "total", "reinvested"},
		Default:     []byte(`"total"`),
		Description: "報酬口徑:price(只看股價)、total(股價加現金股利,預設)、reinvested(股利再投入)",
	}
}

// cagrRankingSchema 描述 get_cagr_ranking 的輸入形狀。
func cagrRankingSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"period": {
				Type:        "string",
				Enum:        []any{"M3", "M6", "Y1", "Y2", "Y3", "Y5", "Y7", "Y10"},
				Default:     []byte(`"Y1"`),
				Description: "統計期間:M3、M6(月)或 Y1～Y10(年),預設 Y1",
			},
			"metric": cagrMetricSchema(),
			"sort": {
				Type:        "string",
				Enum:        []any{"cagr", "total_return"},
				Description: "排序鍵:cagr(年化報酬率)或 total_return(區間總報酬率);省略時一年以下期間用總報酬、其餘用年化",
			},
			"market": listedOTCMarketSchema(),
			"industry_id": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Description: "產業編號篩選(選填)",
			},
			"include_incomplete": {
				Type:        "boolean",
				Default:     []byte("false"),
				Description: "是否列出期初資料不足、無法計算的股票(排在最後),預設 false",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Default:     []byte("20"),
				Description: "最大回傳筆數(預設 20,範圍 1 至 50)",
			},
			"offset": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Default:     []byte("0"),
				Description: "略過前幾筆,供分頁使用(預設 0)",
			},
		},
	}
}

// stockCagrSchema 描述 get_stock_cagr 的輸入形狀。
func stockCagrSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"metric": cagrMetricSchema(),
		},
		Required: []string{"symbol"},
	}
}

// cagrToolset 綁定 CAGR 資料來源與記錄函式。
type cagrToolset struct {
	cagr CagrQuerier
	logf func(string, ...any)
}

// normalizeChoice 套用預設值並以白名單驗證列舉參數。
func normalizeChoice(raw, def, field string, allowed []string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	for _, value := range allowed {
		if strings.EqualFold(strings.TrimSpace(raw), value) {
			return value, nil
		}
	}
	return "", fmt.Errorf("參數 %s 必須為 %s 其中之一,收到了 %q", field, strings.Join(allowed, "、"), raw)
}

// cagrRanking 執行 get_cagr_ranking。
func (ts *cagrToolset) cagrRanking(ctx context.Context, _ *mcp.CallToolRequest, in CagrRankingInput) (*mcp.CallToolResult, any, error) {
	period, err := normalizeChoice(in.Period, "Y1", "period", cagrPeriods)
	if err != nil {
		return nil, nil, err
	}
	metric, err := normalizeChoice(in.Metric, "total", "metric", cagrMetrics)
	if err != nil {
		return nil, nil, err
	}
	sort := ""
	if in.Sort != "" {
		if sort, err = normalizeChoice(in.Sort, "", "sort", []string{"cagr", "total_return"}); err != nil {
			return nil, nil, err
		}
	}
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	if in.IndustryID < 0 {
		return nil, nil, fmt.Errorf("參數 industry_id 必須為正整數")
	}
	limit, err := rangedLimit(in.Limit, 20, 1, 50)
	if err != nil {
		return nil, nil, err
	}
	if in.Offset < 0 {
		return nil, nil, fmt.Errorf("參數 offset 不可為負數")
	}

	ranking, err := ts.cagr.CagrRanking(ctx, CagrRankingOptions{
		Period: period, Metric: metric, Sort: sort, Market: market, IndustryID: in.IndustryID,
		IncludeIncomplete: in.IncludeIncomplete, Limit: limit, Offset: in.Offset,
	})
	if err != nil {
		if errors.Is(err, ErrMarketDataNotFound) {
			return nil, nil, fmt.Errorf("目前查無 CAGR 計算結果")
		}
		ts.logf("工具 get_cagr_ranking 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(cagrRankingSummary(ranking)), CagrRankingOutput{
		DataKind: "cagr_ranking", IsRealtime: false, Disclaimer: AnalysisDisclaimer, CagrRanking: ranking,
	}, nil
}

// stockCagr 執行 get_stock_cagr。
func (ts *cagrToolset) stockCagr(ctx context.Context, _ *mcp.CallToolRequest, in StockCagrInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	metric, err := normalizeChoice(in.Metric, "total", "metric", cagrMetrics)
	if err != nil {
		return nil, nil, err
	}
	result, err := ts.cagr.StockCagr(ctx, symbol, metric)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號 %s,或尚無 CAGR 計算結果", in.Symbol)
		}
		ts.logf("工具 get_stock_cagr 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(stockCagrSummary(result)), StockCagrOutput{
		DataKind: "stock_cagr", IsRealtime: false, Disclaimer: AnalysisDisclaimer, StockCagr: result,
	}, nil
}

func orDash(value *string) string {
	if value == nil || *value == "" {
		return "—"
	}
	return *value
}

// cagrRankingSummary 組出排行摘要:基準日、樣本、正報酬比例與前幾名。
func cagrRankingSummary(r *CagrRanking) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s 期間(口徑 %s、基準日 %s、期初 %s)共 %d 檔符合條件;全市場 %d 檔中 %d 檔可計算,正報酬比例 %s。",
		r.Period, r.Metric, r.Date, orDash(r.BaseDate), r.Total, r.Coverage.Universe, r.Coverage.Counted, r.Summary.PositiveRatio)
	shown := 0
	for _, item := range r.Items {
		if item.Rank == nil || shown == 5 {
			continue
		}
		if shown == 0 {
			b.WriteString("前幾名:")
		} else {
			b.WriteString("、")
		}
		fmt.Fprintf(&b, "%d. %s %s 年化 %s%%(總報酬 %s%%)", *item.Rank, item.StockSymbol, item.Name, orDash(item.CagrPct), orDash(item.TotalReturnPct))
		shown++
	}
	if shown > 0 {
		b.WriteString("。")
	}
	if r.Coverage.SurvivorshipNote {
		b.WriteString("注意:樣本只含目前仍上市櫃的股票,有存活者偏誤。")
	}
	fmt.Fprintf(&b, "\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// stockCagrSummary 列出個股各期間的年化報酬。
func stockCagrSummary(s *StockCagr) string {
	var b strings.Builder
	name := s.StockSymbol
	if s.Name != nil {
		name = s.StockSymbol + " " + *s.Name
	}
	fmt.Fprintf(&b, "%s 各期間年化報酬(口徑 %s、基準日 %s):", name, s.Metric, s.Date)
	for index, item := range s.Items {
		if index > 0 {
			b.WriteString("、")
		}
		if item.CagrPct == nil {
			fmt.Fprintf(&b, "%s 資料不足", item.Period)
			continue
		}
		fmt.Fprintf(&b, "%s %s%%", item.Period, *item.CagrPct)
		if item.HasAnomaly {
			b.WriteString("(有疑似除權或減資跳動)")
		}
	}
	fmt.Fprintf(&b, "。\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
