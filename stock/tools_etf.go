// Package stock 的 screen_etfs 與 get_etf_detail 工具:ETF 專區的清單篩選與單檔明細。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/etfs 與 GET /api/v1/etfs/{symbol}:
// 清單讀每天收盤後整理好的 ETF 快照(行情、預估淨值與折溢價、規模、淨申購、配息、含息報酬、受益人數),
// 明細另含基本資料、市價與淨值含息報酬、風險指標、前十大持股、行業比重、資產分佈與近期每日折溢價。
package stock

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
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

func codes(pairs []struct{ code, label string }) []any {
	values := make([]any, len(pairs))
	for i, pair := range pairs {
		values[i] = pair.code
	}
	return values
}

// screenEtfsSchema 描述 screen_etfs 的輸入形狀;新參數不帶 schema 預設值。
func screenEtfsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"strategy": {
				Type: "array", Items: &jsonschema.Schema{Type: "string", Enum: codes(etfStrategyLabels)},
				Description: "分類(選填,可多選):market_cap 市值型、high_dividend 高股息、active 主動式、bond 債券、leveraged 槓桿、" +
					"inverse 反向、commodity 期貨商品、overseas 海外股票、thematic 主題",
			},
			"region":    {Type: "string", MaxLength: ptr(20), Description: "投資地區(選填),例如 台灣、美國、全球、日本"},
			"frequency": {Type: "string", Enum: []any{"monthly", "quarterly", "semi_annual", "annual", "none"}, Description: "配息頻率(選填)"},
			"market":    {Type: "string", Enum: []any{"all", "twse", "tpex"}, Description: "市場(選填,未提供為 all)"},
			"keyword":   {Type: "string", MaxLength: ptr(40), Description: "關鍵字(選填):比對代號、名稱、全名、發行投信、追蹤指數與題材"},
			"sort":      {Type: "string", Enum: codes(etfSortLabels), Description: "排序鍵(選填,未提供為 trade_value 成交值)"},
			"order":     {Type: "string", Enum: []any{"desc", "asc"}, Description: "排序方向(選填,未提供為 desc 由大到小)"},
			"limit":     {Type: "integer", Minimum: ptr(1.0), Maximum: ptr(100.0), Description: "最多幾檔(選填,1 至 100;未提供為 20)"},
		},
	}
}

// etfDetailSchema 描述 get_etf_detail 的輸入形狀。
func etfDetailSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"symbol"},
		Properties: map[string]*jsonschema.Schema{
			"symbol":   {Type: "string", MinLength: ptr(1), MaxLength: ptr(12), Description: "ETF 代號,例如 0050、00679B"},
			"nav_days": {Type: "integer", Minimum: ptr(1.0), Maximum: ptr(60.0), Description: "近期每日淨值與折溢價的筆數(選填,1 至 60;未提供為 20)"},
		},
	}
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

// labelOf 在代碼表中找中文名稱;找不到時回傳原代碼。
func labelOf(pairs []struct{ code, label string }, code string) string {
	for _, pair := range pairs {
		if pair.code == code {
			return pair.label
		}
	}
	return code
}

// yiText 把元換成億元(小數一位);nil 顯示「無」。
func yiText(v *float64) string {
	if v == nil {
		return "無"
	}
	return formatFloat(math.Round(*v/1e7)/10) + " 億"
}

// signedPercentPtr 顯示帶正負號的百分比;nil 顯示「無」。
func signedPercentPtr(v *float64) string {
	if v == nil {
		return "無"
	}
	return signedPercent(*v)
}

// etfKind 組出「分類、地區、配息頻率、市場」的簡短描述。
func etfKind(item EtfItem) string {
	parts := []string{labelOf(etfStrategyLabels, item.Strategy)}
	if item.Region != "" {
		parts = append(parts, item.Region)
	}
	if label, ok := etfFrequencyLabels[item.DividendFrequency]; ok {
		parts = append(parts, label)
	}
	parts = append(parts, map[string]string{"twse": "上市", "tpex": "上櫃"}[item.Market])
	return strings.Join(parts, "、")
}

// screenEtfsSummary 組出給模型閱讀的清單摘要。
func screenEtfsSummary(e *EtfListEnvelope) string {
	var b summaryBuilder
	order := map[string]string{"desc": "由大到小", "asc": "由小到大"}[e.Order]
	b.printf("符合條件的 ETF 共 %d 檔,依%s%s列出 %d 檔。", e.Total, labelOf(etfSortLabels, e.Sort), order, len(e.Items))
	for i, item := range e.Items {
		if i == etfScreenListed {
			b.printf("\n另有 %d 檔見結構化輸出。", len(e.Items)-etfScreenListed)
			break
		}
		b.printf("\n%d. %s %s(%s):收盤 %s(%s)、成交值 %s、規模 %s、折溢價 %s、近 12 月殖利率 %s、近 1 年含息報酬 %s",
			i+1, item.StockSymbol, item.Name, etfKind(item), displayFloat(item.ClosingPrice), signedPercentPtr(item.ChangeRange),
			yiText(item.TradeValue), yiText(item.TotalAssets), signedPercentPtr(item.PremiumRate), displayPercent(item.Yield12m),
			signedPercentPtr(item.Ret1y))
		if item.NextExDate != nil {
			b.printf("、下次除息 %s(%s 元)", *item.NextExDate, displayFloat(item.NextDividend))
		}
	}
	b.printf("\n折溢價為(市價 − 預估淨值)÷ 預估淨值;規模為已發行單位 × 淨值。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// etfDetailSummary 組出給模型閱讀的單檔摘要。
func etfDetailSummary(e *EtfDetailEnvelope) string {
	var b summaryBuilder
	item := e.Item
	b.printf("%s %s(%s)", item.StockSymbol, item.Name, etfKind(item))
	if e.Profile != nil && e.Profile.FullName != "" {
		b.printf(",全名 %s", e.Profile.FullName)
	}
	b.printf(";發行投信 %s、追蹤指數 %s", orNone(item.Issuer), orNone(item.Benchmark))
	if e.Profile != nil {
		b.printf("、經理費 %s、保管費 %s", orNone(e.Profile.ManagementFee), orNone(e.Profile.CustodianFee))
	}
	b.printf("。\n行情(%s):收盤 %s(%s)、預估淨值 %s、折溢價 %s(近 20 筆平均 %s)、規模 %s、今日淨申購 %s、受益人數 %s。",
		displayString(item.QuoteDate), displayFloat(item.ClosingPrice), signedPercentPtr(item.ChangeRange), displayFloat(item.Nav),
		signedPercentPtr(item.PremiumRate), signedPercentPtr(item.PremiumAvg20d), yiText(item.TotalAssets), yiText(item.NetFlow),
		int64Text(item.Holders))
	b.printf("\n配息:近 12 個月 %s 元、殖利率 %s", displayFloat(item.Dividend12m), displayPercent(item.Yield12m))
	if item.NextExDate != nil {
		b.printf("、下次除息 %s(%s 元)", *item.NextExDate, displayFloat(item.NextDividend))
	}
	b.printf("。")
	if p := e.Performance; p != nil {
		b.printf("\n市價含息報酬(%s):1 月 %s、今年 %s、1 年 %s、3 年年化 %s;近 1 年波動 %s、最大回撤 %s、beta %s、夏普 %s。",
			displayString(p.CalcDate), signedPercentPtr(p.Ret1m), signedPercentPtr(p.RetYtd), signedPercentPtr(p.Ret1y), signedPercentPtr(p.Ret3y),
			displayPercent(p.Vol1y), signedPercentPtr(p.Mdd1y), displayFloat(p.Beta1y), displayFloat(p.Sharpe1y))
		if p.NavCalcDate != nil {
			b.printf("\n淨值含息報酬(%s):1 月 %s、今年 %s、1 年 %s、3 年年化 %s。",
				*p.NavCalcDate, signedPercentPtr(p.NavRet1m), signedPercentPtr(p.NavRetYtd), signedPercentPtr(p.NavRet1y), signedPercentPtr(p.NavRet3y))
		}
	}
	if len(e.Holdings) > 0 {
		names := make([]string, 0, len(e.Holdings))
		for _, h := range e.Holdings {
			names = append(names, fmt.Sprintf("%s %s%%", strings.TrimSpace(h.Symbol+" "+h.Name), formatFloat(h.Weight)))
		}
		b.printf("\n前十大持股(%s):%s。", displayString(e.HoldingsAsOf), strings.Join(names, "、"))
	}
	writeAllocations(&b, "行業比重", e.IndustriesAsOf, e.Industries)
	writeAllocations(&b, "資產分佈", e.AssetsAsOf, e.Assets)
	if len(e.Navs) > 0 {
		var lo, hi *float64
		for _, n := range e.Navs {
			if n.PremiumRate == nil {
				continue
			}
			if lo == nil || *n.PremiumRate < *lo {
				lo = n.PremiumRate
			}
			if hi == nil || *n.PremiumRate > *hi {
				hi = n.PremiumRate
			}
		}
		b.printf("\n近 %d 個交易日折溢價介於 %s 至 %s。", len(e.Navs), signedPercentPtr(lo), signedPercentPtr(hi))
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// writeAllocations 寫出行業比重或資產分佈的前五項。
func writeAllocations(b *summaryBuilder, title string, asOf *string, rows []EtfAllocationItem) {
	if len(rows) == 0 {
		return
	}
	parts := make([]string, 0, 5)
	for _, row := range rows[:min(len(rows), 5)] {
		parts = append(parts, fmt.Sprintf("%s %s%%", row.Label, formatFloat(row.Weight)))
	}
	b.printf("\n%s(%s):%s。", title, displayString(asOf), strings.Join(parts, "、"))
}

// orNone 把空字串顯示成「無」。
func orNone(s string) string {
	if s == "" {
		return "無"
	}
	return s
}

// int64Text 顯示 *int64;nil 顯示「無」。
func int64Text(v *int64) string {
	if v == nil {
		return "無"
	}
	return fmt.Sprint(*v)
}
