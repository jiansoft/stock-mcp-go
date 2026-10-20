// Package stock 的 get_holding_chip 工具:持股籌碼日報。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/chip:某交易日收盤時仍持有的普通股(不含 ETF)
// 當天的三大法人買賣超、法人連續天數、融資融券與主力進出,以及達到 Telegram 通知門檻的原因;
// 與每天的「持股法人與信用交易」「持股主力進出」通知、stock_go 持股籌碼頁共用門檻。
package stock

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HoldingChipBrokerNet 是單一分點的買賣超(張);Net 為非負值,方向由所在的一側決定。
type HoldingChipBrokerNet struct {
	Name  string  `json:"name"`
	Buy   int64   `json:"buy"`
	Sell  int64   `json:"sell"`
	Net   int64   `json:"net"`
	Share float64 `json:"share"`
}

// HoldingChipBroker 是持股當天的主力進出(張);MainShare 為佔成交量比重(%),正值為買超。
type HoldingChipBroker struct {
	MainNet   int64                  `json:"main_net"`
	MainShare float64                `json:"main_share"`
	Buyers    []HoldingChipBrokerNet `json:"buyers"`
	Sellers   []HoldingChipBrokerNet `json:"sellers"`
}

// HoldingChipBrokerRecent 是持股近幾個主力交易日的主力累計(張)。
type HoldingChipBrokerRecent struct {
	MainNet  int64 `json:"main_net"`
	BuyDays  int64 `json:"buy_days"`
	SellDays int64 `json:"sell_days"`
}

// HoldingChipItem 是單一持股當天的籌碼;張數已由股換算成張,來源沒有資料時為 nil。
type HoldingChipItem struct {
	StockSymbol   string             `json:"stock_symbol"`
	Name          string             `json:"name"`
	ClosingPrice  *float64           `json:"closing_price"`
	Volume        *int64             `json:"volume"`
	ForeignNet    *int64             `json:"foreign_net"`
	TrustNet      *int64             `json:"trust_net"`
	DealerNet     *int64             `json:"dealer_net"`
	ForeignShare  *float64           `json:"foreign_share"`
	TrustShare    *float64           `json:"trust_share"`
	ForeignStreak int                `json:"foreign_streak"`
	TrustStreak   int                `json:"trust_streak"`
	MarginBalance *int64             `json:"margin_balance"`
	MarginChange  *int64             `json:"margin_change"`
	ShortBalance  *int64             `json:"short_balance"`
	ShortChange   *int64             `json:"short_change"`
	Broker        *HoldingChipBroker `json:"broker"`
	// BrokerRecent 是含當天最近 HoldingChipEnvelope.BrokerRecentDays 個主力交易日的累計;沒有資料為 nil。
	BrokerRecent *HoldingChipBrokerRecent `json:"broker_recent"`
	Reasons      []string                 `json:"reasons"`
}

// HoldingChipEnvelope 是 portfolio/chip endpoint 的完整回應;完全沒有籌碼資料時 Date 為 nil。
type HoldingChipEnvelope struct {
	Date         *string `json:"date"`
	PreviousDate *string `json:"previous_date"`
	NextDate     *string `json:"next_date"`
	Notable      int     `json:"notable"`
	// BrokerRecentDays 是 BrokerRecent 涵蓋的主力交易日數;舊版 API 沒有這個欄位時為 0。
	BrokerRecentDays int               `json:"broker_recent_days"`
	Items            []HoldingChipItem `json:"items"`
}

// HoldingChipQuerier 是 get_holding_chip 對資料來源的需求介面;只有 *APIClient 實作。
type HoldingChipQuerier interface {
	HoldingChip(ctx context.Context, date string) (*HoldingChipEnvelope, error)
}

// HoldingChip 查詢持股籌碼日報;date 空字串代表最新交易日。
func (c *APIClient) HoldingChip(ctx context.Context, date string) (*HoldingChipEnvelope, error) {
	path := "/api/v1/portfolio/chip"
	if date != "" {
		path += "?" + url.Values{"date": {date}}.Encode()
	}
	var body HoldingChipEnvelope
	if err := c.get(ctx, path, &body); err != nil {
		return nil, err
	}
	if body.Items == nil {
		body.Items = []HoldingChipItem{}
	}
	return &body, nil
}

// HoldingChipInput 是 get_holding_chip 的輸入。
type HoldingChipInput struct {
	Date string `json:"date,omitempty"`
}

// HoldingChipOutput 是持股籌碼 envelope 加上 MCP 分析中繼資料。
type HoldingChipOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*HoldingChipEnvelope
}

// holdingChipSchema 描述 get_holding_chip 的輸入形狀;新參數不帶 schema 預設值。
func holdingChipSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "查詢日(格式 YYYY-MM-DD,選填);取不晚於它的最近一個有籌碼資料的交易日,未提供為最新交易日",
			},
		},
	}
}

// holdingChipToolset 綁定持股籌碼資料來源與記錄函式。
type holdingChipToolset struct {
	hq   HoldingChipQuerier
	logf func(string, ...any)
}

// holdingChip 執行 get_holding_chip。
func (ts *holdingChipToolset) holdingChip(ctx context.Context, _ *mcp.CallToolRequest, in HoldingChipInput) (*mcp.CallToolResult, any, error) {
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	envelope, err := ts.hq.HoldingChip(ctx, date)
	if err != nil {
		ts.logf("工具 get_holding_chip 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(holdingChipSummary(envelope)), HoldingChipOutput{
		DataKind: "holding_chip", IsRealtime: false, Disclaimer: AnalysisDisclaimer, HoldingChipEnvelope: envelope,
	}, nil
}

// holdingChipReasonLabels 是達標原因代碼的中文名稱,與 stock_rust ChipReason::label 一致。
var holdingChipReasonLabels = map[string]string{
	"foreign_heavy":  "外資大量",
	"trust_heavy":    "投信大量",
	"foreign_streak": "外資連續",
	"trust_streak":   "投信連續",
	"margin":         "融資",
	"short":          "融券",
	"broker":         "主力",
}

// holdingChipSummary 組出給模型閱讀的文字摘要:達標持股列出完整籌碼,其餘持股只列法人買賣超。
func holdingChipSummary(e *HoldingChipEnvelope) string {
	if e.Date == nil {
		return "查無持股籌碼資料。"
	}
	var b summaryBuilder
	b.printf("持股籌碼日報 %s(前一交易日 %s、後一交易日 %s):共 %d 檔持股(普通股,不含 ETF),%d 檔達通知門檻。",
		*e.Date, displayString(e.PreviousDate), displayString(e.NextDate), len(e.Items), e.Notable)
	var quiet []string
	for _, item := range e.Items {
		if len(item.Reasons) == 0 {
			quiet = append(quiet, fmt.Sprintf("%s %s 外資 %s/投信 %s", item.StockSymbol, item.Name,
				lotsText(item.ForeignNet), lotsText(item.TrustNet)))
			continue
		}
		b.printf("\n%s", holdingChipItemText(item, e.BrokerRecentDays))
	}
	if len(quiet) > 0 {
		b.printf("\n未達門檻(張):%s。", strings.Join(quiet, "、"))
	}
	b.printf("\n張數由股換算(四捨五入);佔量為買賣超佔當日成交量。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// holdingChipItemText 描述一檔達標持股的法人、信用交易與主力進出。
func holdingChipItemText(item HoldingChipItem, recentDays int) string {
	labels := make([]string, 0, len(item.Reasons))
	for _, code := range item.Reasons {
		if label, ok := holdingChipReasonLabels[code]; ok {
			labels = append(labels, label)
		} else {
			labels = append(labels, code)
		}
	}
	var b summaryBuilder
	b.printf("%s %s(收盤 %s,成交 %s 張)〔%s〕:外資 %s%s、投信 %s%s、自營 %s 張;融資餘額 %s(%s)、融券餘額 %s(%s)",
		item.StockSymbol, item.Name, displayFloat(item.ClosingPrice), intText(item.Volume), strings.Join(labels, "、"),
		lotsText(item.ForeignNet), flowDetail(item.ForeignShare, item.ForeignStreak),
		lotsText(item.TrustNet), flowDetail(item.TrustShare, item.TrustStreak),
		intText(item.DealerNet), intText(item.MarginBalance), signedLots(item.MarginChange),
		intText(item.ShortBalance), signedLots(item.ShortChange))
	if broker := item.Broker; broker != nil {
		b.printf(";主力 %s 張(佔量 %s%%)", signedText(broker.MainNet), formatFloat(broker.MainShare))
		if text := brokerNetsText(broker.Buyers); text != "" {
			b.printf(",買超分點 %s", text)
		}
		if text := brokerNetsText(broker.Sellers); text != "" {
			b.printf(",賣超分點 %s", text)
		}
	}
	if recent := item.BrokerRecent; recent != nil && recentDays > 1 {
		b.printf(";近 %d 日主力 %s 張(買超 %d 日、賣超 %d 日)", recentDays, signedText(recent.MainNet), recent.BuyDays, recent.SellDays)
	}
	b.printf("。")
	return b.String()
}

// lotsText 顯示帶正負號的張數;nil 顯示「無」。
func lotsText(v *int64) string {
	if v == nil {
		return "無"
	}
	return signedText(*v) + " 張"
}

// flowDetail 把佔成交量比重與連續買賣超天數放進括號;兩者都沒有時不顯示。
func flowDetail(share *float64, streak int) string {
	var parts []string
	if share != nil {
		parts = append(parts, fmt.Sprintf("佔量 %s%%", formatFloat(*share)))
	}
	switch {
	case streak > 0:
		parts = append(parts, fmt.Sprintf("連買 %d 日", streak))
	case streak < 0:
		parts = append(parts, fmt.Sprintf("連賣 %d 日", -streak))
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ",") + ")"
}

// holdingChipTopBrokers 是摘要每一側列出的分點數;完整清單在結構化輸出。
const holdingChipTopBrokers = 3

// brokerNetsText 把前 holdingChipTopBrokers 名分點買賣超串成「名稱 張數」清單。
func brokerNetsText(nets []HoldingChipBrokerNet) string {
	nets = nets[:min(len(nets), holdingChipTopBrokers)]
	parts := make([]string, 0, len(nets))
	for _, net := range nets {
		parts = append(parts, fmt.Sprintf("%s %d 張", net.Name, net.Net))
	}
	return strings.Join(parts, "、")
}
