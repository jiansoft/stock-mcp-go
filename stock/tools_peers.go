// Package stock 的 get_stock_peers 工具:查詢單一股票與同產業個股的比較。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/peers:同產業(上市櫃、未下市、
// 不含 ETF、近 30 天有成交)個股的市值、本益比、股價淨值比、殖利率、F 分數、毛利率、
// 最新月營收年增率,以及各指標的產業中位數與目標股名次。市值單位為新台幣元。
package stock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PeerStock 是單一同業個股的指標;沒有資料或已過期的欄位為 nil。
type PeerStock struct {
	StockSymbol          string   `json:"stock_symbol"`
	Name                 string   `json:"name"`
	MarketID             int      `json:"market_id"`
	ClosingPrice         *float64 `json:"closing_price"`
	MarketCap            *float64 `json:"market_cap"`
	PriceEarningRatio    *float64 `json:"price_earning_ratio"`
	PriceToBookRatio     *float64 `json:"price_to_book_ratio"`
	DividendYieldPercent *float64 `json:"dividend_yield_percent"`
	FScore               *int     `json:"f_score"`
	GrossMarginPercent   *float64 `json:"gross_margin_percent"`
	RevenueYOYPercent    *float64 `json:"revenue_yoy_percent"`
}

// PeerMetric 是單一指標在同業間的位置;Better 為 lower(越低越好)或 higher。
type PeerMetric struct {
	Metric         string   `json:"metric"`
	Better         string   `json:"better"`
	Target         *float64 `json:"target"`
	IndustryMedian *float64 `json:"industry_median"`
	Rank           *int     `json:"rank"`
	Samples        int      `json:"samples"`
}

// PeersEnvelope 是 peers endpoint 的完整回應;目標股近 30 天沒有報價時 Target 為 nil。
type PeersEnvelope struct {
	StockSymbol  string       `json:"stock_symbol"`
	IndustryID   int          `json:"industry_id"`
	IndustryName string       `json:"industry_name"`
	DataAsOf     *string      `json:"data_as_of"`
	PeerCount    int          `json:"peer_count"`
	Target       *PeerStock   `json:"target"`
	Metrics      []PeerMetric `json:"metrics"`
	Peers        []PeerStock  `json:"peers"`
}

// PeersQuerier 是 get_stock_peers 對資料來源的需求介面;只有 *APIClient 實作。
type PeersQuerier interface {
	StockPeers(ctx context.Context, symbol string, limit int) (*PeersEnvelope, error)
}

// StockPeers 查詢同業比較;404 只代表股票不存在。
func (c *APIClient) StockPeers(ctx context.Context, symbol string, limit int) (*PeersEnvelope, error) {
	values := url.Values{"limit": []string{fmt.Sprint(limit)}}
	var body PeersEnvelope
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/peers?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	return &body, nil
}

// PeersInput 是 get_stock_peers 的輸入。
type PeersInput struct {
	Symbol string `json:"symbol"`
	Limit  int    `json:"limit,omitempty"`
}

// PeersOutput 是同業比較 envelope 加上 MCP 分析中繼資料。
type PeersOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*PeersEnvelope
}

// peersSchema 描述 get_stock_peers 的輸入形狀。
func peersSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100.0),
				Default:     []byte("20"),
				Description: "依市值由大到小列出幾檔同業(預設 20,範圍 1 至 100);中位數與名次一律用全部同業計算",
			},
		},
		Required: []string{"symbol"},
	}
}

// peersToolset 綁定同業比較資料來源與記錄函式。
type peersToolset struct {
	peers PeersQuerier
	logf  func(string, ...any)
}

// stockPeers 執行 get_stock_peers。
func (ts *peersToolset) stockPeers(ctx context.Context, _ *mcp.CallToolRequest, in PeersInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	limit, err := rangedLimit(in.Limit, 20, 1, 100)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 limit 必須介於 1 到 100 之間")
	}
	envelope, err := ts.peers.StockPeers(ctx, symbol, limit)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_stock_peers 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(peersSummary(envelope)), PeersOutput{
		DataKind: "stock_peers", IsRealtime: false, Disclaimer: AnalysisDisclaimer, PeersEnvelope: envelope,
	}, nil
}

// peerMetricLabels 是各指標的中文名稱與單位。
var peerMetricLabels = map[string][2]string{
	"market_cap":             {"市值", " 億元"},
	"price_earning_ratio":    {"本益比", " 倍"},
	"price_to_book_ratio":    {"股價淨值比", " 倍"},
	"dividend_yield_percent": {"殖利率", "%"},
	"f_score":                {"F 分數", " 分"},
	"gross_margin_percent":   {"毛利率", "%"},
	"revenue_yoy_percent":    {"月營收年增率", "%"},
}

// peersSummary 組出給模型閱讀的文字摘要:目標股、各指標名次、市值前幾大同業。
func peersSummary(e *PeersEnvelope) string {
	var b summaryBuilder
	industry := e.IndustryName
	if industry == "" {
		industry = "未分類"
	}
	b.printf("股票 %s 所屬產業「%s」共 %d 檔同業(資料日 %s)。", e.StockSymbol, industry, e.PeerCount, displayString(e.DataAsOf))
	if e.Target == nil {
		b.printf("\n%s 近 30 天沒有報價,沒有名次。", e.StockSymbol)
	} else {
		b.printf("\n%s %s:%s。", e.Target.StockSymbol, e.Target.Name, peerStockText(e.Target))
	}
	b.printf("\n各指標在同業的位置:")
	for _, m := range e.Metrics {
		label, ok := peerMetricLabels[m.Metric]
		if !ok {
			label = [2]string{m.Metric, ""}
		}
		direction := "越高越好"
		if m.Better == "lower" {
			direction = "越低越好"
		}
		target, median := m.Target, m.IndustryMedian
		if m.Metric == "market_cap" {
			target, median = capToHundredMillion(target), capToHundredMillion(median)
		}
		rank := "無名次"
		if m.Rank != nil {
			rank = fmt.Sprintf("第 %d／%d 名", *m.Rank, m.Samples)
		}
		b.printf("\n- %s(%s):目標 %s%s,%s,產業中位數 %s%s", label[0], direction,
			displayFloat(target), label[1], rank, displayFloat(median), label[1])
	}
	if len(e.Peers) > 0 {
		b.printf("\n市值前 %d 檔:", len(e.Peers))
		for i := range e.Peers {
			p := &e.Peers[i]
			b.printf("\n- %s %s:%s", p.StockSymbol, p.Name, peerStockText(p))
		}
	}
	b.printf("\n本益比、股價淨值比只比較大於 0 的值(虧損不列入);名次同值同名。")
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// peerStockText 把單一股票的指標串成一行。
func peerStockText(p *PeerStock) string {
	fScore := "無"
	if p.FScore != nil {
		fScore = fmt.Sprint(*p.FScore)
	}
	return fmt.Sprintf("市值 %s 億元、本益比 %s、股價淨值比 %s、殖利率 %s%%、F 分數 %s、毛利率 %s%%、月營收年增 %s%%",
		displayFloat(capToHundredMillion(p.MarketCap)), displayFloat(p.PriceEarningRatio), displayFloat(p.PriceToBookRatio),
		displayFloat(p.DividendYieldPercent), fScore, displayFloat(p.GrossMarginPercent), displayFloat(p.RevenueYOYPercent))
}

// capToHundredMillion 把元換算成億元(四捨五入到小數 2 位);nil 原樣回傳。
func capToHundredMillion(v *float64) *float64 {
	if v == nil {
		return nil
	}
	return ptr(math.Round(*v/1e6) / 100)
}
