// Package stock 的 get_chip_data 工具:查詢單一股票的籌碼面。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/chip,
// 一次取得每日三大法人與融資融券、外資投信連續買賣超、千張大戶週資料、
// 董監持股設質與最近一天的券商分點主力進出。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ChipDay 是單一交易日的三大法人買賣超(股)與融資融券餘額(張)。
// 來源當天沒有該類資料時欄位為 nil(例如不能信用交易的股票沒有融資融券)。
type ChipDay struct {
	Date          string `json:"date"`
	ForeignNet    *int64 `json:"foreign_net"`
	TrustNet      *int64 `json:"trust_net"`
	DealerNet     *int64 `json:"dealer_net"`
	TotalNet      *int64 `json:"total_net"`
	MarginBalance *int64 `json:"margin_balance"`
	MarginChange  *int64 `json:"margin_change"`
	ShortBalance  *int64 `json:"short_balance"`
	ShortChange   *int64 `json:"short_change"`
}

// ChipStreak 是外資、投信從最新一天往回數的連續同向天數:正值連買、負值連賣。
type ChipStreak struct {
	ForeignDays int64 `json:"foreign_days"`
	TrustDays   int64 `json:"trust_days"`
}

// HolderWeek 是集保戶股權分散的單週摘要。
type HolderWeek struct {
	Date         string   `json:"date"`
	MajorHolders int64    `json:"major_holders"`
	MajorPercent *float64 `json:"major_percent"`
	TotalHolders int64    `json:"total_holders"`
}

// InsiderSummary 是最新月份董監事持股與設質的合計。
type InsiderSummary struct {
	Month          string   `json:"month"`
	Insiders       int64    `json:"insiders"`
	Shares         int64    `json:"shares"`
	Pledged        int64    `json:"pledged"`
	RelatedPledged int64    `json:"related_pledged"`
	PledgePercent  *float64 `json:"pledge_percent"`
}

// BrokerNet 是單一券商分點的買賣超(張)。
type BrokerNet struct {
	Name  string   `json:"name"`
	Buy   int64    `json:"buy"`
	Sell  int64    `json:"sell"`
	Net   int64    `json:"net"`
	Share *float64 `json:"share"`
}

// BrokerFlowDay 是最近一天的券商分點主力進出(只有持股才有資料)。
type BrokerFlowDay struct {
	Date      string      `json:"date"`
	MainNet   int64       `json:"main_net"`
	MainShare *float64    `json:"main_share"`
	Buyers    []BrokerNet `json:"buyers"`
	Sellers   []BrokerNet `json:"sellers"`
}

// ChipEnvelope 是 chip endpoint 的完整回應。
type ChipEnvelope struct {
	StockSymbol        string          `json:"stock_symbol"`
	DataAsOf           *string         `json:"data_as_of"`
	Daily              []ChipDay       `json:"daily"`
	Streak             ChipStreak      `json:"streak"`
	HolderDistribution []HolderWeek    `json:"holder_distribution"`
	Insider            *InsiderSummary `json:"insider"`
	BrokerFlow         *BrokerFlowDay  `json:"broker_flow"`
}

// ChipQuerier 是 get_chip_data 對資料來源的需求介面;只有 *APIClient 實作,
// 只實作基本 Querier 的資料來源不會出現這個工具。
type ChipQuerier interface {
	StockChip(ctx context.Context, symbol string, days int) (*ChipEnvelope, error)
}

// StockChip 查詢個股籌碼;404 只代表股票不存在,空陣列與 null 原樣保留。
func (c *APIClient) StockChip(ctx context.Context, symbol string, days int) (*ChipEnvelope, error) {
	path := "/api/v1/stocks/" + url.PathEscape(symbol) + "/chip?" + url.Values{"days": []string{fmt.Sprint(days)}}.Encode()
	var body ChipEnvelope
	found, err := c.getFound(ctx, path, &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	if body.Daily == nil {
		body.Daily = []ChipDay{}
	}
	if body.HolderDistribution == nil {
		body.HolderDistribution = []HolderWeek{}
	}
	return &body, nil
}

// ChipInput 是 get_chip_data 的輸入。
type ChipInput struct {
	Symbol string `json:"symbol"`
	Days   int    `json:"days,omitempty"`
}

// ChipOutput 是籌碼 envelope 加上 MCP 分析中繼資料。
type ChipOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*ChipEnvelope
}

// chipSchema 描述 get_chip_data 的輸入形狀。
func chipSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"days": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(120.0),
				Default:     []byte("20"),
				Description: "每日籌碼回傳的交易日數(預設 20,範圍 1 至 120)",
			},
		},
		Required: []string{"symbol"},
	}
}

// chipToolset 綁定籌碼資料來源與記錄函式。
type chipToolset struct {
	chips ChipQuerier
	logf  func(string, ...any)
}

// stockChip 執行 get_chip_data。
func (ts *chipToolset) stockChip(ctx context.Context, _ *mcp.CallToolRequest, in ChipInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	days, err := rangedLimit(in.Days, 20, 1, 120)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 120 之間")
	}
	envelope, err := ts.chips.StockChip(ctx, symbol, days)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_chip_data 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(chipSummary(envelope)), ChipOutput{
		DataKind: "stock_chip", IsRealtime: false, Disclaimer: AnalysisDisclaimer, ChipEnvelope: envelope,
	}, nil
}

// chipSummary 組出給模型閱讀的文字摘要;數字單位與交易所一致(法人股數換算成張)。
func chipSummary(e *ChipEnvelope) string {
	var b strings.Builder
	if len(e.Daily) == 0 {
		fmt.Fprintf(&b, "股票 %s 尚無每日籌碼資料。", e.StockSymbol)
	} else {
		d := e.Daily[0]
		fmt.Fprintf(&b, "股票 %s 於 %s:外資 %s、投信 %s、自營商 %s,三大法人合計 %s;",
			e.StockSymbol, d.Date, lots(d.ForeignNet), lots(d.TrustNet), lots(d.DealerNet), lots(d.TotalNet))
		fmt.Fprintf(&b, "外資%s、投信%s。", streakText(e.Streak.ForeignDays), streakText(e.Streak.TrustDays))
		if d.MarginBalance != nil {
			fmt.Fprintf(&b, "融資餘額 %d 張(%s)、融券餘額 %s 張(%s)。",
				*d.MarginBalance, signedLots(d.MarginChange), intText(d.ShortBalance), signedLots(d.ShortChange))
		}
	}
	if len(e.HolderDistribution) > 0 {
		h := e.HolderDistribution[0]
		fmt.Fprintf(&b, "千張大戶 %s 持股 %s%%(%d 人)。", h.Date, displayFloat(h.MajorPercent), h.MajorHolders)
	}
	if e.Insider != nil {
		fmt.Fprintf(&b, "董監 %s 設質比例 %s%%。", e.Insider.Month, displayFloat(e.Insider.PledgePercent))
	}
	if e.BrokerFlow != nil {
		fmt.Fprintf(&b, "主力 %s %s 張(佔成交量 %s%%)。", e.BrokerFlow.Date, signedText(e.BrokerFlow.MainNet), displayFloat(e.BrokerFlow.MainShare))
	}
	fmt.Fprintf(&b, "\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// lots 把買賣超股數換成帶正負號的張數文字;無資料回「無資料」。
func lots(shares *int64) string {
	if shares == nil {
		return "無資料"
	}
	return signedText(*shares/1000) + " 張"
}

func signedLots(v *int64) string {
	if v == nil {
		return "增減無資料"
	}
	return signedText(*v) + " 張"
}

func signedText(v int64) string {
	if v > 0 {
		return fmt.Sprintf("+%d", v)
	}
	return fmt.Sprintf("%d", v)
}

func intText(v *int64) string {
	if v == nil {
		return "無資料"
	}
	return fmt.Sprintf("%d", *v)
}

func streakText(days int64) string {
	switch {
	case days > 0:
		return fmt.Sprintf("連續買超 %d 天", days)
	case days < 0:
		return fmt.Sprintf("連續賣超 %d 天", -days)
	default:
		return "最新一天無買賣超"
	}
}
