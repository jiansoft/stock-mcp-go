// Package stock 的 get_technical_indicators 工具:查詢單一股票的技術指標與訊號。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/technicals:截止日之前最近
// days 個交易日的均線(5/10/20/60/120/240)、乖離率、RSI(6/12/14)、KD(9,3,3)、MACD(12,26,9),
// 以及最新一日的均線/KD/MACD 交叉、收盤站上或跌破月季年線與超買超賣訊號。價格口徑依回應的 adjusted 說明。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TechnicalPoint 是單日的技術指標;樣本不足的欄位為 nil。
type TechnicalPoint struct {
	Date          string   `json:"date"`
	Close         float64  `json:"close"`
	MA5           *float64 `json:"ma5"`
	MA10          *float64 `json:"ma10"`
	MA20          *float64 `json:"ma20"`
	MA60          *float64 `json:"ma60"`
	MA120         *float64 `json:"ma120"`
	MA240         *float64 `json:"ma240"`
	Bias5         *float64 `json:"bias5"`
	Bias20        *float64 `json:"bias20"`
	Bias60        *float64 `json:"bias60"`
	Bias240       *float64 `json:"bias240"`
	RSI6          *float64 `json:"rsi6"`
	RSI12         *float64 `json:"rsi12"`
	RSI14         *float64 `json:"rsi14"`
	K             *float64 `json:"k"`
	D             *float64 `json:"d"`
	MACDDif       *float64 `json:"macd_dif"`
	MACDSignal    *float64 `json:"macd_signal"`
	MACDHistogram *float64 `json:"macd_histogram"`
}

// TechnicalSignal 是最新一日的技術訊號。
type TechnicalSignal struct {
	Indicator   string `json:"indicator"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// TechnicalsEnvelope 是 technicals endpoint 的完整回應;截止日前沒有日線時 DataAsOf 為 nil。
type TechnicalsEnvelope struct {
	StockSymbol   string            `json:"stock_symbol"`
	DataAsOf      *string           `json:"data_as_of"`
	Days          int               `json:"days"`
	Adjusted      bool              `json:"adjusted"`
	Points        []TechnicalPoint  `json:"points"`
	Signals       []TechnicalSignal `json:"signals"`
	RangeBreakout *RangeBreakout    `json:"range_breakout"`
}

// RangeBreakout 是最新一日相對前 30 日整理區間的狀態(還原價,成交量不還原);資料不足時 envelope 內為 nil。
type RangeBreakout struct {
	PivotPrice       float64 `json:"pivot_price"`
	BaseLow          float64 `json:"base_low"`
	BaseWidthPercent float64 `json:"base_width_percent"`
	AverageVolume    float64 `json:"average_volume"`
	VolumeRatio      float64 `json:"volume_ratio"`
	DistancePercent  float64 `json:"distance_percent"`
	BreaksOut        bool    `json:"breaks_out"`
}

// TechnicalsQuerier 是 get_technical_indicators 對資料來源的需求介面;只有 *APIClient 實作。
type TechnicalsQuerier interface {
	TechnicalIndicators(ctx context.Context, symbol, date string, days int) (*TechnicalsEnvelope, error)
}

// TechnicalIndicators 查詢個股技術指標;date 空字串代表最新交易日,404 只代表股票不存在。
func (c *APIClient) TechnicalIndicators(ctx context.Context, symbol, date string, days int) (*TechnicalsEnvelope, error) {
	values := url.Values{"days": []string{fmt.Sprint(days)}}
	if date != "" {
		values.Set("date", date)
	}
	var body TechnicalsEnvelope
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/technicals?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	return &body, nil
}

// TechnicalsInput 是 get_technical_indicators 的輸入。
type TechnicalsInput struct {
	Symbol string `json:"symbol"`
	Date   string `json:"date,omitempty"`
	Days   int    `json:"days,omitempty"`
}

// TechnicalsOutput 是技術指標 envelope 加上 MCP 分析中繼資料。
type TechnicalsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*TechnicalsEnvelope
}

// technicalsSchema 描述 get_technical_indicators 的輸入形狀。
func technicalsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "截止交易日(格式 YYYY-MM-DD,選填);未提供取最新交易日,遇休市日取之前最近的交易日",
			},
			"days": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(120.0),
				Default:     []byte("20"),
				Description: "回傳最近幾個交易日的指標(預設 20,範圍 1 至 120);文字摘要只列最近 10 日",
			},
		},
		Required: []string{"symbol"},
	}
}

// technicalsToolset 綁定技術指標資料來源與記錄函式。
type technicalsToolset struct {
	technicals TechnicalsQuerier
	logf       func(string, ...any)
}

// technicalIndicators 執行 get_technical_indicators。
func (ts *technicalsToolset) technicalIndicators(ctx context.Context, _ *mcp.CallToolRequest, in TechnicalsInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	days, err := rangedLimit(in.Days, 20, 1, 120)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 120 之間")
	}
	envelope, err := ts.technicals.TechnicalIndicators(ctx, symbol, date, days)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_technical_indicators 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(technicalsSummary(envelope)), TechnicalsOutput{
		DataKind: "technical_indicators", IsRealtime: false, Disclaimer: AnalysisDisclaimer, TechnicalsEnvelope: envelope,
	}, nil
}

// technicalsRecentLines 是文字摘要列出的最近交易日數上限。
const technicalsRecentLines = 10

// technicalsSummary 組出給模型閱讀的文字摘要:最新一日的完整指標、均線排列、訊號與近幾日走勢。
func technicalsSummary(e *TechnicalsEnvelope) string {
	var b summaryBuilder
	if e.DataAsOf == nil || len(e.Points) == 0 {
		b.printf("股票 %s 在指定日期之前沒有日線資料。", e.StockSymbol)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	last := &e.Points[len(e.Points)-1]
	basis := "價格未還原除權息,除權息日可能出現跳空"
	if e.Adjusted {
		basis = "價格已還原權值,除權息、減資、分割之前的價格依參考價調整,最新一日為實際價格"
	}
	b.printf("股票 %s 技術指標(資料日 %s,%s)。", e.StockSymbol, *e.DataAsOf, basis)
	b.printf("\n收盤 %s 元;均線 MA5 %s、MA10 %s、MA20 %s、MA60 %s、MA120 %s、MA240 %s。",
		formatFloat(last.Close), displayFloat(last.MA5), displayFloat(last.MA10), displayFloat(last.MA20),
		displayFloat(last.MA60), displayFloat(last.MA120), displayFloat(last.MA240))
	b.printf("\n乖離率:5 日 %s、20 日 %s、60 日 %s、240 日 %s。",
		displayPercent(last.Bias5), displayPercent(last.Bias20), displayPercent(last.Bias60), displayPercent(last.Bias240))
	b.printf("\nRSI6 %s、RSI12 %s、RSI14 %s;K %s、D %s;MACD DIF %s、訊號線 %s、柱狀體 %s。",
		displayFloat(last.RSI6), displayFloat(last.RSI12), displayFloat(last.RSI14), displayFloat(last.K), displayFloat(last.D),
		displayFloat(last.MACDDif), displayFloat(last.MACDSignal), displayFloat(last.MACDHistogram))
	if trend := maAlignment(last); trend != "" {
		b.printf("\n%s", trend)
	}
	if len(e.Signals) == 0 {
		b.printf("\n最新一日沒有交叉或超買超賣訊號。")
	} else {
		b.printf("\n最新一日訊號:")
		for _, signal := range e.Signals {
			b.printf("\n- %s", signal.Description)
		}
	}
	if line := rangeBreakoutLine(e.RangeBreakout); line != "" {
		b.printf("\n%s", line)
	}
	start := max(len(e.Points)-technicalsRecentLines, 0)
	b.printf("\n近 %d 個交易日:", len(e.Points)-start)
	for i := start; i < len(e.Points); i++ {
		p := &e.Points[i]
		b.printf("\n- %s 收 %s、K %s、D %s、RSI14 %s、MACD 柱 %s",
			p.Date, formatFloat(p.Close), displayFloat(p.K), displayFloat(p.D), displayFloat(p.RSI14), displayFloat(p.MACDHistogram))
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// maAlignment 依 MA5、MA20、MA60 判斷均線排列;任一缺值時回空字串。
func maAlignment(p *TechnicalPoint) string {
	if p.MA5 == nil || p.MA20 == nil || p.MA60 == nil {
		return ""
	}
	short, mid, long := *p.MA5, *p.MA20, *p.MA60
	switch {
	case short > mid && mid > long:
		return "均線多頭排列(MA5 > MA20 > MA60)。"
	case short < mid && mid < long:
		return "均線空頭排列(MA5 < MA20 < MA60)。"
	default:
		return "均線糾結(MA5、MA20、MA60 未呈單一方向排列)。"
	}
}

// rangeBreakoutLine 把區間突破狀態寫成一行;沒有資料時回空字串。
func rangeBreakoutLine(r *RangeBreakout) string {
	if r == nil {
		return ""
	}
	verdict := "未成立"
	if r.BreaksOut {
		verdict = "成立"
	}
	return fmt.Sprintf("區間突破%s:前 30 日整理區間 %.2f~%.2f 元(幅度 %.2f%%),量比 %.2f 倍,收盤距突破價 %.2f%%"+
		"(條件:幅度 ≤ 20%%、收盤 > 突破價、量比 ≥ 1.5、距突破價 ≤ 5%%)",
		verdict, r.BaseLow, r.PivotPrice, r.BaseWidthPercent, r.VolumeRatio, r.DistancePercent)
}
