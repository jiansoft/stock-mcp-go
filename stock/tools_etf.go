// Package stock 的 screen_etfs、get_etf_detail 與 compare_etfs 工具:ETF 專區的清單篩選、單檔明細與並排比較。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/etfs、GET /api/v1/etfs/{symbol} 與 GET /api/v1/etfs/compare:
// 清單讀每天收盤後整理好的 ETF 快照(行情、預估淨值與折溢價、規模、淨申購、配息、含息報酬、受益人數),
// 明細另含基本資料、市價與淨值含息報酬、風險指標、前十大持股、行業比重、資產分佈與近期每日折溢價。
package stock

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// etfScreenListed 是 screen_etfs 摘要最多列出的檔數;完整清單在結構化輸出。
const etfScreenListed = 20

// etfStrategyLabels 是分類代碼的中文名稱(順序即 schema enum 順序)。
var etfStrategyLabels = []struct{ code, label string }{
	{"market_cap", "市值型"}, {"high_dividend", "高股息"}, {"active", "主動式"}, {"bond", "債券"},
	{"leveraged", "槓桿"}, {"inverse", "反向"}, {"commodity", "期貨商品"}, {"overseas", "海外股票"}, {"thematic", "主題"},
}

// etfFrequencyLabels 是配息頻率的中文名稱。
var etfFrequencyLabels = map[string]string{
	"monthly": "月配", "quarterly": "季配", "semi_annual": "半年配", "annual": "年配", "none": "不配息",
}

// etfSortLabels 是排序鍵的中文名稱(順序即 schema enum 順序)。
var etfSortLabels = []struct{ code, label string }{
	{"trade_value", "成交值"}, {"total_assets", "資產規模"}, {"premium_rate", "折溢價"}, {"yield_12m", "近 12 個月殖利率"},
	{"net_flow", "淨申購金額"}, {"ret_1m", "近 1 月含息報酬"}, {"ret_ytd", "今年以來含息報酬"}, {"ret_1y", "近 1 年含息報酬"},
	{"holders", "受益人數"},
}

// EtfItem 是一檔 ETF 的概況;比率為 %,金額為元。
type EtfItem struct {
	StockSymbol       string   `json:"stock_symbol"`
	Name              string   `json:"name"`
	Market            string   `json:"market"`
	Strategy          string   `json:"strategy"`
	Region            string   `json:"region"`
	Themes            []string `json:"themes"`
	DividendFrequency string   `json:"dividend_frequency"`
	Issuer            string   `json:"issuer"`
	Benchmark         string   `json:"benchmark"`
	QuoteDate         *string  `json:"quote_date"`
	ClosingPrice      *float64 `json:"closing_price"`
	ChangeRange       *float64 `json:"change_range"`
	TradeValue        *float64 `json:"trade_value"`
	NavDate           *string  `json:"nav_date"`
	Nav               *float64 `json:"nav"`
	PremiumRate       *float64 `json:"premium_rate"`
	PremiumAvg20d     *float64 `json:"premium_avg_20d"`
	TotalAssets       *float64 `json:"total_assets"`
	NetFlow           *float64 `json:"net_flow"`
	Dividend12m       *float64 `json:"dividend_12m"`
	Yield12m          *float64 `json:"yield_12m"`
	NextExDate        *string  `json:"next_ex_date"`
	NextDividend      *float64 `json:"next_dividend"`
	Ret1m             *float64 `json:"ret_1m"`
	RetYtd            *float64 `json:"ret_ytd"`
	Ret1y             *float64 `json:"ret_1y"`
	Holders           *int64   `json:"holders"`
	ForeignNet        *int64   `json:"foreign_net"`
}

// EtfListEnvelope 是 /api/v1/etfs 的回應。
type EtfListEnvelope struct {
	Total int64     `json:"total"`
	Sort  string    `json:"sort"`
	Order string    `json:"order"`
	Items []EtfItem `json:"items"`
}

// EtfProfileInfo 是 ETF 基本資料。
type EtfProfileInfo struct {
	FullName      string  `json:"full_name"`
	InceptionDate *string `json:"inception_date"`
	IsActive      bool    `json:"is_active"`
	ManagementFee string  `json:"management_fee"`
	CustodianFee  string  `json:"custodian_fee"`
	Currency      string  `json:"currency"`
	Source        string  `json:"source"`
}

// EtfPerformanceInfo 是 ETF 績效;報酬為 %,3 年以上年化。
type EtfPerformanceInfo struct {
	CalcDate    *string  `json:"calc_date"`
	Ret1w       *float64 `json:"ret_1w"`
	Ret1m       *float64 `json:"ret_1m"`
	Ret3m       *float64 `json:"ret_3m"`
	Ret6m       *float64 `json:"ret_6m"`
	RetYtd      *float64 `json:"ret_ytd"`
	Ret1y       *float64 `json:"ret_1y"`
	Ret3y       *float64 `json:"ret_3y"`
	Ret5y       *float64 `json:"ret_5y"`
	Vol1y       *float64 `json:"vol_1y"`
	Mdd1y       *float64 `json:"mdd_1y"`
	Beta1y      *float64 `json:"beta_1y"`
	Sharpe1y    *float64 `json:"sharpe_1y"`
	NavCalcDate *string  `json:"nav_calc_date"`
	NavRet1m    *float64 `json:"nav_ret_1m"`
	NavRetYtd   *float64 `json:"nav_ret_ytd"`
	NavRet1y    *float64 `json:"nav_ret_1y"`
	NavRet3y    *float64 `json:"nav_ret_3y"`
}

// EtfHoldingItem 是前十大持股的一列。
type EtfHoldingItem struct {
	Symbol string  `json:"symbol"`
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
}

// EtfAllocationItem 是行業比重或資產分佈的一列。
type EtfAllocationItem struct {
	Label  string  `json:"label"`
	Weight float64 `json:"weight"`
}

// EtfNavPoint 是近期一天的淨值與折溢價。
type EtfNavPoint struct {
	Date        string   `json:"date"`
	Nav         *float64 `json:"nav"`
	MarketPrice *float64 `json:"market_price"`
	PremiumRate *float64 `json:"premium_rate"`
	UnitsChange *int64   `json:"units_change"`
}

// EtfDetailEnvelope 是 /api/v1/etfs/{symbol} 的回應。
type EtfDetailEnvelope struct {
	Item           EtfItem             `json:"item"`
	Profile        *EtfProfileInfo     `json:"profile"`
	Performance    *EtfPerformanceInfo `json:"performance"`
	HoldingsAsOf   *string             `json:"holdings_as_of"`
	Holdings       []EtfHoldingItem    `json:"holdings"`
	IndustriesAsOf *string             `json:"industries_as_of"`
	Industries     []EtfAllocationItem `json:"industries"`
	AssetsAsOf     *string             `json:"assets_as_of"`
	Assets         []EtfAllocationItem `json:"assets"`
	Navs           []EtfNavPoint       `json:"navs"`
}

// EtfCompareItem 是比較表的一檔 ETF;比率為 %,金額為元,報酬 3 年以上年化。
type EtfCompareItem struct {
	StockSymbol       string   `json:"stock_symbol"`
	Name              string   `json:"name"`
	Strategy          string   `json:"strategy"`
	Benchmark         string   `json:"benchmark"`
	ManagementFee     string   `json:"management_fee"`
	CustodianFee      string   `json:"custodian_fee"`
	TotalAssets       *float64 `json:"total_assets"`
	ClosingPrice      *float64 `json:"closing_price"`
	PremiumRate       *float64 `json:"premium_rate"`
	PremiumAvg20d     *float64 `json:"premium_avg_20d"`
	Yield12m          *float64 `json:"yield_12m"`
	DividendFrequency string   `json:"dividend_frequency"`
	Ret1y             *float64 `json:"ret_1y"`
	Ret3y             *float64 `json:"ret_3y"`
	NavRet1y          *float64 `json:"nav_ret_1y"`
	Vol1y             *float64 `json:"vol_1y"`
	Mdd1y             *float64 `json:"mdd_1y"`
	Sharpe1y          *float64 `json:"sharpe_1y"`
	HoldingsAsOf      *string  `json:"holdings_as_of"`
}

// EtfOverlap 是兩檔 ETF 前十大持股的重疊。
type EtfOverlap struct {
	A             string   `json:"a"`
	B             string   `json:"b"`
	Common        []string `json:"common"`
	OverlapWeight float64  `json:"overlap_weight"`
}

// EtfCompareEnvelope 是 /api/v1/etfs/compare 的回應。
type EtfCompareEnvelope struct {
	Items    []EtfCompareItem `json:"items"`
	Overlaps []EtfOverlap     `json:"overlaps"`
}

// EtfScreenParams 是 ETF 清單的查詢條件;零值欄位不送出,交給 Data API 的預設值。
type EtfScreenParams struct {
	Strategies []string
	Region     string
	Frequency  string
	Market     string
	Keyword    string
	Sort       string
	Order      string
	Limit      int
}

// EtfQuerier 是 ETF 工具對資料來源的需求介面;只有 *APIClient 實作。
type EtfQuerier interface {
	ScreenEtfs(ctx context.Context, params EtfScreenParams) (*EtfListEnvelope, error)
	// EtfDetail 查無這檔 ETF 時回傳 nil, nil。
	EtfDetail(ctx context.Context, symbol string, navDays int) (*EtfDetailEnvelope, error)
	// CompareEtfs 其中有查無的 ETF 時回傳 nil, nil。
	CompareEtfs(ctx context.Context, symbols []string) (*EtfCompareEnvelope, error)
}

// ScreenEtfs 查詢 ETF 清單。
func (c *APIClient) ScreenEtfs(ctx context.Context, params EtfScreenParams) (*EtfListEnvelope, error) {
	values := url.Values{}
	set := func(key, value string) {
		if value != "" {
			values.Set(key, value)
		}
	}
	set("strategy", strings.Join(params.Strategies, ","))
	set("region", params.Region)
	set("frequency", params.Frequency)
	set("market", params.Market)
	set("keyword", params.Keyword)
	set("sort", params.Sort)
	set("order", params.Order)
	if params.Limit > 0 {
		values.Set("limit", fmt.Sprint(params.Limit))
	}
	path := "/api/v1/etfs"
	if len(values) > 0 {
		path += "?" + values.Encode()
	}
	var body EtfListEnvelope
	if err := c.get(ctx, path, &body); err != nil {
		return nil, err
	}
	if body.Items == nil {
		body.Items = []EtfItem{}
	}
	return &body, nil
}

// EtfDetail 查詢單檔 ETF;404 代表不是上市櫃 ETF。
func (c *APIClient) EtfDetail(ctx context.Context, symbol string, navDays int) (*EtfDetailEnvelope, error) {
	path := "/api/v1/etfs/" + url.PathEscape(symbol)
	if navDays > 0 {
		path += "?" + url.Values{"nav_days": {fmt.Sprint(navDays)}}.Encode()
	}
	var body EtfDetailEnvelope
	found, err := c.getFound(ctx, path, &body)
	if err != nil || !found {
		return nil, err
	}
	for _, list := range []*[]EtfAllocationItem{&body.Industries, &body.Assets} {
		if *list == nil {
			*list = []EtfAllocationItem{}
		}
	}
	if body.Holdings == nil {
		body.Holdings = []EtfHoldingItem{}
	}
	if body.Navs == nil {
		body.Navs = []EtfNavPoint{}
	}
	return &body, nil
}

// CompareEtfs 比較 2 至 4 檔 ETF;404 代表其中有不是上市櫃 ETF 的代號。
func (c *APIClient) CompareEtfs(ctx context.Context, symbols []string) (*EtfCompareEnvelope, error) {
	var body EtfCompareEnvelope
	found, err := c.getFound(ctx, "/api/v1/etfs/compare?"+url.Values{"symbols": {strings.Join(symbols, ",")}}.Encode(), &body)
	if err != nil || !found {
		return nil, err
	}
	if body.Items == nil {
		body.Items = []EtfCompareItem{}
	}
	if body.Overlaps == nil {
		body.Overlaps = []EtfOverlap{}
	}
	for i := range body.Overlaps {
		if body.Overlaps[i].Common == nil {
			body.Overlaps[i].Common = []string{}
		}
	}
	return &body, nil
}

// CompareEtfsInput 是 compare_etfs 的輸入。
type CompareEtfsInput struct {
	Symbols []string `json:"symbols"`
}

// CompareEtfsOutput 是 ETF 比較加上 MCP 分析中繼資料。
type CompareEtfsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*EtfCompareEnvelope
}

// compareEtfs 執行 compare_etfs。
func (ts *etfToolset) compareEtfs(ctx context.Context, _ *mcp.CallToolRequest, in CompareEtfsInput) (*mcp.CallToolResult, any, error) {
	if len(in.Symbols) < 2 || len(in.Symbols) > 4 {
		return nil, nil, fmt.Errorf("參數 symbols 必須是 2 到 4 檔 ETF 代號")
	}
	symbols := make([]string, 0, len(in.Symbols))
	for _, raw := range in.Symbols {
		symbol, err := normalizeSymbol(raw)
		if err != nil {
			return nil, nil, err
		}
		if slices.Contains(symbols, symbol) {
			return nil, nil, fmt.Errorf("參數 symbols 不可重複:%s", symbol)
		}
		symbols = append(symbols, symbol)
	}
	envelope, err := ts.eq.CompareEtfs(ctx, symbols)
	if err != nil {
		ts.logf("工具 compare_etfs 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	if envelope == nil {
		return nil, nil, fmt.Errorf("%s 之中有查無的 ETF;可用 screen_etfs 以名稱或關鍵字找代號", strings.Join(symbols, "、"))
	}
	return textResult(compareEtfsSummary(envelope)), CompareEtfsOutput{
		DataKind: "etf_compare", IsRealtime: false, Disclaimer: AnalysisDisclaimer, EtfCompareEnvelope: envelope,
	}, nil
}

// ScreenEtfsInput 是 screen_etfs 的輸入。
type ScreenEtfsInput struct {
	Strategy  []string `json:"strategy,omitempty"`
	Region    string   `json:"region,omitempty"`
	Frequency string   `json:"frequency,omitempty"`
	Market    string   `json:"market,omitempty"`
	Keyword   string   `json:"keyword,omitempty"`
	Sort      string   `json:"sort,omitempty"`
	Order     string   `json:"order,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

// EtfDetailInput 是 get_etf_detail 的輸入。
type EtfDetailInput struct {
	Symbol  string `json:"symbol"`
	NavDays int    `json:"nav_days,omitempty"`
}

// ScreenEtfsOutput 是 ETF 清單加上 MCP 分析中繼資料。
type ScreenEtfsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*EtfListEnvelope
}

// EtfDetailOutput 是 ETF 明細加上 MCP 分析中繼資料。
type EtfDetailOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*EtfDetailEnvelope
}

// etfToolset 綁定 ETF 資料來源與記錄函式。
type etfToolset struct {
	eq   EtfQuerier
	logf func(string, ...any)
}

// screenEtfs 執行 screen_etfs。
func (ts *etfToolset) screenEtfs(ctx context.Context, _ *mcp.CallToolRequest, in ScreenEtfsInput) (*mcp.CallToolResult, any, error) {
	if in.Limit != 0 && (in.Limit < 1 || in.Limit > 100) {
		return nil, nil, fmt.Errorf("參數 limit 必須介於 1 到 100 之間")
	}
	envelope, err := ts.eq.ScreenEtfs(ctx, EtfScreenParams{
		Strategies: in.Strategy, Region: strings.TrimSpace(in.Region), Frequency: in.Frequency, Market: in.Market,
		Keyword: strings.TrimSpace(in.Keyword), Sort: in.Sort, Order: in.Order, Limit: in.Limit,
	})
	if err != nil {
		ts.logf("工具 screen_etfs 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(screenEtfsSummary(envelope)), ScreenEtfsOutput{
		DataKind: "etf_screen", IsRealtime: false, Disclaimer: AnalysisDisclaimer, EtfListEnvelope: envelope,
	}, nil
}

// etfDetail 執行 get_etf_detail。
func (ts *etfToolset) etfDetail(ctx context.Context, _ *mcp.CallToolRequest, in EtfDetailInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	if in.NavDays != 0 && (in.NavDays < 1 || in.NavDays > 60) {
		return nil, nil, fmt.Errorf("參數 nav_days 必須介於 1 到 60 之間")
	}
	envelope, err := ts.eq.EtfDetail(ctx, symbol, in.NavDays)
	if err != nil {
		ts.logf("工具 get_etf_detail 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	if envelope == nil {
		return nil, nil, fmt.Errorf("查無 ETF %s;可用 screen_etfs 以名稱或關鍵字找代號", symbol)
	}
	return textResult(etfDetailSummary(envelope)), EtfDetailOutput{
		DataKind: "etf_detail", IsRealtime: false, Disclaimer: AnalysisDisclaimer, EtfDetailEnvelope: envelope,
	}, nil
}
