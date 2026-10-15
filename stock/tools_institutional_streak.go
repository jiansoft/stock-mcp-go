// Package stock 的 get_institutional_streak_performance 工具:法人連續買賣超之後的報酬統計。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/market/institutional-streak/performance:某檔股票的法人
// 連續買超(或賣超)剛好滿 N 天的那一天算一次事件,統計事件之後 5/20/60/120/240 個交易日的還原報酬
// 平均、中位數、勝率與相對大盤的超額報酬。統計方式與 get_signal_performance 相同。
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

// InstitutionalStreakEnvelope 是 institutional-streak/performance endpoint 的完整回應。
type InstitutionalStreakEnvelope struct {
	From        string          `json:"from"`
	To          string          `json:"to"`
	Market      string          `json:"market"`
	Investor    string          `json:"investor"`
	Side        string          `json:"side"`
	Streak      int             `json:"streak"`
	Industry    *string         `json:"industry"`
	Adjusted    bool            `json:"adjusted"`
	Occurrences int             `json:"occurrences"`
	Horizons    []HorizonStat   `json:"horizons"`
	Benchmark   []BenchmarkStat `json:"benchmark"`
}

// InstitutionalStreakOptions 是法人連續買賣超報酬統計的查詢條件;From、To、Industry 空字串代表不指定。
type InstitutionalStreakOptions struct {
	From     string
	To       string
	Investor string
	Side     string
	Streak   int
	Market   string
	Industry string
}

// InstitutionalStreakQuerier 是 get_institutional_streak_performance 對資料來源的需求介面;只有 *APIClient 實作。
type InstitutionalStreakQuerier interface {
	InstitutionalStreakPerformance(ctx context.Context, opt InstitutionalStreakOptions) (*InstitutionalStreakEnvelope, error)
}

// InstitutionalStreakPerformance 查詢法人連續買賣超後的報酬統計;404 代表迄日之前沒有籌碼資料,
// 422 代表產業名稱不存在(其餘參數在工具層已驗證)。
func (c *APIClient) InstitutionalStreakPerformance(ctx context.Context, opt InstitutionalStreakOptions) (*InstitutionalStreakEnvelope, error) {
	values := url.Values{
		"investor": []string{opt.Investor},
		"side":     []string{opt.Side},
		"streak":   []string{fmt.Sprint(opt.Streak)},
		"market":   []string{opt.Market},
	}
	for key, value := range map[string]string{"from": opt.From, "to": opt.To, "industry": opt.Industry} {
		if value != "" {
			values.Set(key, value)
		}
	}
	var body InstitutionalStreakEnvelope
	found, err := c.getFound(ctx, "/api/v1/market/institutional-streak/performance?"+values.Encode(), &body)
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

// InstitutionalStreakInput 是 get_institutional_streak_performance 的輸入。
type InstitutionalStreakInput struct {
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Investor string `json:"investor,omitempty"`
	Side     string `json:"side,omitempty"`
	Streak   int    `json:"streak,omitempty"`
	Market   string `json:"market,omitempty"`
	Industry string `json:"industry,omitempty"`
}

// InstitutionalStreakOutput 是法人連續買賣超報酬統計 envelope 加上 MCP 分析中繼資料。
type InstitutionalStreakOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*InstitutionalStreakEnvelope
}

// institutionalStreakSchema 描述 get_institutional_streak_performance 的輸入形狀。
func institutionalStreakSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"from": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "統計起日(格式 YYYY-MM-DD,選填);未提供為迄日往前 364 天",
			},
			"to": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "統計迄日(格式 YYYY-MM-DD,選填);未提供為最新有籌碼資料的交易日。起訖最長 1,096 天",
			},
			"investor": investorSchema(),
			"side": {
				Type:        "string",
				Enum:        []any{"buy", "sell"},
				Default:     []byte(`"buy"`),
				Description: "buy(預設,連續買超)或 sell(連續賣超)",
			},
			"streak": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(20.0),
				Default:     []byte("3"),
				Description: "連續天數 N(預設 3,範圍 1 至 20):連續買超(或賣超)剛好滿 N 天的那一天算一次事件,之後延續的天數不重複計算",
			},
			"market": listedOTCMarketSchema(),
			"industry": {
				Type:        "string",
				MaxLength:   ptr(20),
				Description: "只統計這個產業的事件(選填),需為完整產業名稱,例如「半導體業」;大盤基準不受影響",
			},
		},
	}
}

// institutionalStreakToolset 綁定法人連續買賣超資料來源與記錄函式。
type institutionalStreakToolset struct {
	sq   InstitutionalStreakQuerier
	logf func(string, ...any)
}

// institutionalStreakPerformance 執行 get_institutional_streak_performance。
func (ts *institutionalStreakToolset) institutionalStreakPerformance(ctx context.Context, _ *mcp.CallToolRequest, in InstitutionalStreakInput) (*mcp.CallToolResult, any, error) {
	from, err := parseOptionalDate(in.From, "from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parseOptionalDate(in.To, "to")
	if err != nil {
		return nil, nil, err
	}
	if err := validatePerformancePeriod(from, to); err != nil {
		return nil, nil, err
	}
	investor, err := normalizeInvestor(in.Investor)
	if err != nil {
		return nil, nil, err
	}
	side := strings.ToLower(strings.TrimSpace(in.Side))
	if side == "" {
		side = "buy"
	}
	if side != "buy" && side != "sell" {
		return nil, nil, fmt.Errorf("參數 side 必須為 buy 或 sell,收到了 %q", in.Side)
	}
	streak, err := rangedLimit(in.Streak, 3, 1, 20)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 streak 必須介於 1 到 20 之間")
	}
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	industry := strings.TrimSpace(in.Industry)
	if err := validateLength(industry, "industry", 0, 20); err != nil {
		return nil, nil, err
	}
	envelope, err := ts.sq.InstitutionalStreakPerformance(ctx, InstitutionalStreakOptions{
		From: from, To: to, Investor: investor, Side: side, Streak: streak, Market: market, Industry: industry,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrMarketDataNotFound):
			return nil, nil, fmt.Errorf("指定迄日之前查無籌碼資料")
		case errors.Is(err, ErrIndustryNotFound):
			return nil, nil, fmt.Errorf("找不到產業「%s」,請使用完整產業名稱,例如「半導體業」", industry)
		}
		ts.logf("工具 get_institutional_streak_performance 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(institutionalStreakSummary(envelope)), InstitutionalStreakOutput{
		DataKind: "institutional_streak_performance", IsRealtime: false, Disclaimer: AnalysisDisclaimer, InstitutionalStreakEnvelope: envelope,
	}, nil
}

// institutionalStreakSummary 組出給模型閱讀的文字摘要:條件、事件數、大盤基準與各天期表現。
func institutionalStreakSummary(e *InstitutionalStreakEnvelope) string {
	var b summaryBuilder
	side := "買超"
	if e.Side == "sell" {
		side = "賣超"
	}
	scope := e.Market + " 市場"
	if e.Industry != nil {
		scope += "、產業「" + *e.Industry + "」"
	}
	b.printf("%s 連續%s滿 %d 天之後的表現(%s～%s,%s;報酬為事件當天收盤到之後第 N 個交易日收盤,%s;超額報酬以同日同市場全部股票的平均為基準):共 %d 次事件。",
		investorLabels[e.Investor], side, e.Streak, e.From, e.To, scope, returnBasisText(e.Adjusted), e.Occurrences)
	if len(e.Benchmark) > 0 {
		b.printf("\n大盤基準:%s。", benchmarkText(e.Benchmark))
	}
	if e.Occurrences == 0 {
		b.printf("\n期間內沒有符合條件的事件。")
	} else {
		parts := make([]string, 0, len(e.Horizons))
		for _, h := range e.Horizons {
			parts = append(parts, horizonText(h))
		}
		b.printf("\n事件之後:%s。", strings.Join(parts, ";"))
	}
	b.printf("\n籌碼資料從 2022-10 起,之前的事件不列入。過去表現不代表未來結果。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
