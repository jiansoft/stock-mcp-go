package stock

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 具名策略的實盤追蹤:stock_rust 在每個換股日 22:10 通知時存下當天的持股,
// GET /api/v1/strategy-presets/{name}/track 依這些紀錄逐期計算實際表現(未扣成本)並與 0050 比較。

// ErrPresetNotFound 表示具名策略不存在,且沒有任何換股紀錄(Data API 回 404)。
var ErrPresetNotFound = errors.New("strategy preset not found")

// StrategyTrackHolding 是一期持股的一檔股票。
type StrategyTrackHolding struct {
	Symbol        string   `json:"symbol"`
	Name          *string  `json:"name"`
	Weight        float64  `json:"weight"`
	ReturnPercent *float64 `json:"return_percent"`
}

// StrategyTrackPeriod 是一次換股(一期)的實際表現。
type StrategyTrackPeriod struct {
	Date            string                 `json:"date"`
	End             string                 `json:"end"`
	Ongoing         bool                   `json:"ongoing"`
	HoldDays        int                    `json:"hold_days"`
	EtfWeight       int                    `json:"etf_weight"`
	Cash            float64                `json:"cash"`
	MarketFiltered  bool                   `json:"market_filtered"`
	Holdings        []StrategyTrackHolding `json:"holdings"`
	StrategyReturn  *float64               `json:"strategy_return"`
	EtfReturn       *float64               `json:"etf_return"`
	PortfolioReturn *float64               `json:"portfolio_return"`
}

// StrategyTrackEnvelope 是 strategy-presets/{name}/track endpoint 的完整回應。
type StrategyTrackEnvelope struct {
	Preset              string                `json:"preset"`
	From                *string               `json:"from"`
	To                  *string               `json:"to"`
	CumulativeReturn    *float64              `json:"cumulative_return"`
	EtfCumulativeReturn *float64              `json:"etf_cumulative_return"`
	ExcessReturn        *float64              `json:"excess_return"`
	Periods             []StrategyTrackPeriod `json:"periods"`
}

// StrategyTrackQuerier 是 get_strategy_track 對資料來源的需求介面;只有 *APIClient 實作。
type StrategyTrackQuerier interface {
	StrategyTrack(ctx context.Context, preset string) (*StrategyTrackEnvelope, error)
}

// StrategyTrack 查詢具名策略的實盤追蹤;404 代表策略不存在且沒有紀錄。
func (c *APIClient) StrategyTrack(ctx context.Context, preset string) (*StrategyTrackEnvelope, error) {
	var body StrategyTrackEnvelope
	found, err := c.getFound(ctx, "/api/v1/strategy-presets/"+url.PathEscape(preset)+"/track", &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrPresetNotFound
	}
	return &body, nil
}

// StrategyTrackInput 是 get_strategy_track 的輸入。
type StrategyTrackInput struct {
	Preset string `json:"preset"`
	// Holdings 為 true 時摘要列出每期的全部持股;預設只列報酬最好與最差的各 3 檔。
	Holdings bool `json:"holdings,omitempty"`
}

// StrategyTrackOutput 是實盤追蹤 envelope 加上 MCP 分析中繼資料。
type StrategyTrackOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*StrategyTrackEnvelope
}

// strategyTrackSchema 描述 get_strategy_track 的輸入形狀。
func strategyTrackSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"preset": {
				Type:        "string",
				MinLength:   ptr(1),
				MaxLength:   ptr(64),
				Description: "具名策略名稱(可用 list_strategy_presets 查看),例如「最佳組合」",
			},
			"holdings": {
				Type:        "boolean",
				Description: "選填;true 時摘要列出每期全部持股與報酬,預設只列每期報酬最好與最差的各 3 檔",
			},
		},
		Required: []string{"preset"},
	}
}

// strategyTrackToolset 綁定實盤追蹤資料來源與記錄函式。
type strategyTrackToolset struct {
	tq   StrategyTrackQuerier
	logf func(string, ...any)
}

// strategyTrack 執行 get_strategy_track。
func (ts *strategyTrackToolset) strategyTrack(ctx context.Context, _ *mcp.CallToolRequest, in StrategyTrackInput) (*mcp.CallToolResult, any, error) {
	preset := strings.TrimSpace(in.Preset)
	if err := validateLength(preset, "preset", 1, 64); err != nil {
		return nil, nil, err
	}
	envelope, err := ts.tq.StrategyTrack(ctx, preset)
	if err != nil {
		if errors.Is(err, ErrPresetNotFound) {
			return nil, nil, fmt.Errorf("找不到具名策略「%s」(可用 list_strategy_presets 查看名稱)", preset)
		}
		ts.logf("工具 get_strategy_track 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(strategyTrackSummary(envelope, in.Holdings)), StrategyTrackOutput{
		DataKind: "strategy_track", IsRealtime: false, Disclaimer: AnalysisDisclaimer, StrategyTrackEnvelope: envelope,
	}, nil
}

// strategyTrackSummary 組出實盤追蹤摘要:整體累積與 0050 比較,再一期一段。
func strategyTrackSummary(e *StrategyTrackEnvelope, allHoldings bool) string {
	var b summaryBuilder
	if len(e.Periods) == 0 {
		b.printf("具名策略「%s」還沒有換股紀錄:每個換股日 22:10 發換股通知時才會記錄當天的持股(或由 stock_rust 手動補記目前這一期)。", e.Preset)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	b.printf("具名策略「%s」實盤追蹤(依換股通知當天記錄的持股,還原報酬、未扣交易成本):%s～%s 共 %d 期,累積 %s,同期持有 0050 %s,超額 %s 個百分點。",
		e.Preset, displayString(e.From), displayString(e.To), len(e.Periods),
		displayPercent(e.CumulativeReturn), displayPercent(e.EtfCumulativeReturn), displayFloat(e.ExcessReturn))
	for _, period := range e.Periods {
		status := ""
		if period.Ongoing {
			status = "(持有中)"
		}
		b.printf("\n- %s → %s%s:組合 %s(策略部位 %s、0050 %s", period.Date, period.End, status,
			displayPercent(period.PortfolioReturn), displayPercent(period.StrategyReturn), displayPercent(period.EtfReturn))
		if period.EtfWeight > 0 {
			b.printf(",0050 核心 %d%%", period.EtfWeight)
		}
		if period.Cash > 0 {
			b.printf(",策略部位現金 %s%%", formatFloat(period.Cash))
		}
		b.printf(")")
		if period.MarketFiltered {
			b.printf(";大盤濾網沒過,策略部位空手")
			continue
		}
		b.printf(";持股 %d 檔", len(period.Holdings))
		if lines := trackHoldingTexts(period.Holdings, allHoldings); len(lines) > 0 {
			b.printf(":%s", strings.Join(lines, ";"))
		}
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// trackHoldingTexts 列出持股(代號、名稱、權重、報酬):全部,或報酬最好與最差的各 3 檔。
func trackHoldingTexts(holdings []StrategyTrackHolding, all bool) []string {
	text := func(h StrategyTrackHolding) string {
		name := ""
		if h.Name != nil {
			name = " " + *h.Name
		}
		return fmt.Sprintf("%s%s %s%% %s", h.Symbol, name, formatFloat(h.Weight), displayPercent(h.ReturnPercent))
	}
	if all || len(holdings) <= 6 {
		lines := make([]string, 0, len(holdings))
		for _, holding := range holdings {
			lines = append(lines, text(holding))
		}
		return lines
	}
	known := make([]StrategyTrackHolding, 0, len(holdings))
	for _, holding := range holdings {
		if holding.ReturnPercent != nil {
			known = append(known, holding)
		}
	}
	if len(known) == 0 {
		return nil
	}
	sorted := slices.Clone(known)
	slices.SortStableFunc(sorted, func(a, b StrategyTrackHolding) int {
		return cmp.Compare(*b.ReturnPercent, *a.ReturnPercent)
	})
	best := make([]string, 0, 3)
	worst := make([]string, 0, 3)
	for i := 0; i < len(sorted) && i < 3; i++ {
		best = append(best, text(sorted[i]))
		if j := len(sorted) - 1 - i; j >= 3 {
			worst = append(worst, text(sorted[j]))
		}
	}
	lines := []string{"最好 " + strings.Join(best, "、")}
	if len(worst) > 0 {
		lines = append(lines, "最差 "+strings.Join(worst, "、"))
	}
	return lines
}
