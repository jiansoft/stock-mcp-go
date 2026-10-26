// Package stock 測試持股再平衡與目標配置之 MCP 工具與 API Client。
package stock

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeRebalanceQuerier struct {
	fakeQuerier
	rebalanceResult *PortfolioRebalance
	targetsResult   *PortfolioTargets
	err             error
	lastRebalance   RebalanceOptions
	lastSetTargets  SetTargetsOptions
	lastGetTargets  struct {
		memberID  int64
		dimension string
	}
}

func (f *fakeRebalanceQuerier) PortfolioRebalance(_ context.Context, opt RebalanceOptions) (*PortfolioRebalance, error) {
	f.lastRebalance = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.rebalanceResult, nil
}

func (f *fakeRebalanceQuerier) SetPortfolioTargets(_ context.Context, opt SetTargetsOptions) (*PortfolioTargets, error) {
	f.lastSetTargets = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.targetsResult, nil
}

func (f *fakeRebalanceQuerier) GetPortfolioTargets(_ context.Context, memberID int64, dimension string) (*PortfolioTargets, error) {
	f.lastGetTargets.memberID = memberID
	f.lastGetTargets.dimension = dimension
	if f.err != nil {
		return nil, f.err
	}
	return f.targetsResult, nil
}

func sampleRebalanceStock() *PortfolioRebalance {
	return &PortfolioRebalance{
		MemberID:            0,
		Dimension:           "stock",
		TotalEquityValue:    5000000.0,
		TotalPortfolioValue: 6000000.0,
		TargetCashPercent:   10.0,
		TolerancePercent:    2.0,
		LotSize:             1000,
		StockItems: []StockRebalanceItem{
			{
				Symbol:           "2330",
				Name:             "台積電",
				CurrentShares:    5000,
				CurrentPrice:     1000.0,
				CurrentValue:     5000000.0,
				CurrentWeight:    83.33,
				TargetWeight:     60.0,
				DeviationPercent: -23.33,
				Action:           "sell",
				SuggestedShares:  1000,
				SuggestedAmount:  1000000.0,
				EstimatedCost:    4425.0,
				NetAmount:        995575.0,
			},
			{
				Symbol:           "2317",
				Name:             "鴻海",
				CurrentShares:    0,
				CurrentPrice:     200.0,
				CurrentValue:     0.0,
				CurrentWeight:    0.0,
				TargetWeight:     30.0,
				DeviationPercent: 30.0,
				Action:           "buy",
				SuggestedShares:  9000,
				SuggestedAmount:  1800000.0,
				EstimatedCost:    2565.0,
				NetAmount:        1802565.0,
			},
		},
		CashSummary: RebalanceCashSummary{
			InitialCash:            1000000.0,
			EstimatedSellNet:       995575.0,
			EstimatedBuyTotal:      1802565.0,
			AvailableCapital:       1995575.0,
			RequiredAdditionalCash: 0.0,
			RemainingCash:          193010.0,
		},
		Disclaimer: RebalanceDisclaimer,
	}
}

// TestGetPortfolioRebalance_StockDimension 驗證個股維度試算文字摘要與免責聲明。
func TestGetPortfolioRebalance_StockDimension(t *testing.T) {
	fake := &fakeRebalanceQuerier{
		rebalanceResult: sampleRebalanceStock(),
	}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	res, out, err := ts.getPortfolioRebalance(context.Background(), nil, RebalanceInput{
		Dimension:        "stock",
		LotSize:          1000,
		TolerancePercent: ptr(2.0),
		AvailableCash:    1000000.0,
	})
	if err != nil {
		t.Fatalf("非預期的錯誤: %v", err)
	}

	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, "持股再平衡試算建議") {
		t.Errorf("摘要缺少標題: %s", content)
	}
	if !strings.Contains(content, "2330 台積電") || !strings.Contains(content, "賣出") {
		t.Errorf("缺少 2330 賣出建議: %s", content)
	}
	if !strings.Contains(content, "2317 鴻海") || !strings.Contains(content, "買入") {
		t.Errorf("缺少 2317 買入建議: %s", content)
	}
	if !strings.Contains(content, RebalanceDisclaimer) {
		t.Errorf("缺少免責聲明: %s", content)
	}

	output, ok := out.(PortfolioRebalanceOutput)
	if !ok || output.Disclaimer != RebalanceDisclaimer {
		t.Errorf("結構化輸出免責聲明不符: %+v", out)
	}
	if output.PortfolioRebalance.TotalPortfolioValue != 6000000.0 {
		t.Errorf("總資產不符: %f", output.PortfolioRebalance.TotalPortfolioValue)
	}
}

// TestGetPortfolioRebalance_IndustryDimension 驗證產業維度試算輸出。
func TestGetPortfolioRebalance_IndustryDimension(t *testing.T) {
	sample := &PortfolioRebalance{
		MemberID:            0,
		Dimension:           "industry",
		TotalEquityValue:    1000000.0,
		TotalPortfolioValue: 1000000.0,
		TargetCashPercent:   0.0,
		TolerancePercent:    2.0,
		LotSize:             1000,
		IndustryItems: []IndustryRebalanceItem{
			{
				Industry:         "半導體業",
				CurrentValue:     1000000.0,
				CurrentWeight:    100.0,
				TargetWeight:     50.0,
				DeviationPercent: -50.0,
				CandidateStocks:  []string{"2330"},
			},
			{
				Industry:         "電腦及週邊設備業",
				CurrentValue:     0.0,
				CurrentWeight:    0.0,
				TargetWeight:     50.0,
				DeviationPercent: 50.0,
				CandidateStocks:  []string{},
			},
		},
		CashSummary: RebalanceCashSummary{
			InitialCash:      0.0,
			AvailableCapital: 0.0,
		},
		Disclaimer: RebalanceDisclaimer,
	}

	fake := &fakeRebalanceQuerier{rebalanceResult: sample}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	res, _, err := ts.getPortfolioRebalance(context.Background(), nil, RebalanceInput{
		Dimension: "industry",
	})
	if err != nil {
		t.Fatalf("非預期的錯誤: %v", err)
	}

	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, "產業配置偏離") {
		t.Errorf("缺少產業配置偏離標題: %s", content)
	}
	if !strings.Contains(content, "半導體業") || !strings.Contains(content, "2330") {
		t.Errorf("缺少半導體業候選標的: %s", content)
	}
	if !strings.Contains(content, RebalanceDisclaimer) {
		t.Errorf("缺少免責聲明: %s", content)
	}
}

// TestGetPortfolioRebalance_CashShortage 驗證現金不足時顯示需新增資金。
func TestGetPortfolioRebalance_CashShortage(t *testing.T) {
	sample := &PortfolioRebalance{
		MemberID:            0,
		Dimension:           "stock",
		TotalEquityValue:    0.0,
		TotalPortfolioValue: 500000.0,
		TargetCashPercent:   0.0,
		TolerancePercent:    2.0,
		LotSize:             1000,
		StockItems: []StockRebalanceItem{
			{
				Symbol:          "2330",
				Name:            "台積電",
				CurrentShares:   0,
				CurrentPrice:    1000.0,
				Action:          "buy",
				SuggestedShares: 1000,
				SuggestedAmount: 1000000.0,
			},
		},
		CashSummary: RebalanceCashSummary{
			InitialCash:            500000.0,
			AvailableCapital:       500000.0,
			EstimatedBuyTotal:      1001425.0,
			RequiredAdditionalCash: 501425.0,
			RemainingCash:          0.0,
		},
		Disclaimer: RebalanceDisclaimer,
	}

	fake := &fakeRebalanceQuerier{rebalanceResult: sample}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	res, _, err := ts.getPortfolioRebalance(context.Background(), nil, RebalanceInput{
		Dimension:     "stock",
		AvailableCash: 500000.0,
	})
	if err != nil {
		t.Fatalf("非預期的錯誤: %v", err)
	}

	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, "需額外新增資金") {
		t.Errorf("現金不足時應包含需額外新增資金提示: %s", content)
	}
}

// TestSetPortfolioTargets_Validation 驗證目標配置參數檢查。
func TestSetPortfolioTargets_Validation(t *testing.T) {
	fake := &fakeRebalanceQuerier{}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	// 1. 權重總和超過 100%
	_, _, err := ts.setPortfolioTargets(context.Background(), nil, SetTargetsInput{
		Dimension: "stock",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: 70.0},
			{TargetKey: "2317", TargetPercent: 40.0},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "不得超過 100%") {
		t.Errorf("應拒絕總和超過 100%%，實際: %v", err)
	}

	// 2. 負值權重
	_, _, err = ts.setPortfolioTargets(context.Background(), nil, SetTargetsInput{
		Dimension: "stock",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: -5.0},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "必須介於 0% 至 100%") {
		t.Errorf("應拒絕負數權重，實際: %v", err)
	}

	// 3. 不合法維度
	_, _, err = ts.setPortfolioTargets(context.Background(), nil, SetTargetsInput{
		Dimension: "invalid_dim",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: 50.0},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "必須為 stock") {
		t.Errorf("應拒絕不合法維度，實際: %v", err)
	}
}

// TestSetPortfolioTargets_Success 驗證設定成功與免責聲明。
func TestSetPortfolioTargets_Success(t *testing.T) {
	sample := &PortfolioTargets{
		MemberID:  0,
		Dimension: "stock",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: 60.0},
			{TargetKey: "2317", TargetPercent: 30.0},
		},
		TargetCashPercent: ptr(10.0),
		Disclaimer:        TargetDisclaimer,
	}

	fake := &fakeRebalanceQuerier{targetsResult: sample}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	res, out, err := ts.setPortfolioTargets(context.Background(), nil, SetTargetsInput{
		Dimension: "stock",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: 60.0},
			{TargetKey: "2317", TargetPercent: 30.0},
		},
	})
	if err != nil {
		t.Fatalf("非預期的錯誤: %v", err)
	}

	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, "目標配置設定完成") {
		t.Errorf("摘要缺少設定完成文字: %s", content)
	}
	if !strings.Contains(content, "2330: 60.00%") || !strings.Contains(content, "2317: 30.00%") {
		t.Errorf("缺少標的目標比例: %s", content)
	}
	if !strings.Contains(content, TargetDisclaimer) {
		t.Errorf("缺少免責聲明: %s", content)
	}

	output := out.(PortfolioTargetsOutput)
	if output.Disclaimer != TargetDisclaimer {
		t.Errorf("結構化輸出免責聲明不符: %+v", out)
	}
}

// TestAPIClient_PortfolioRebalance_And_Targets 測試 APIClient 與 HTTP 端點通訊。
func TestAPIClient_PortfolioRebalance_And_Targets(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/portfolio/rebalance":
			if r.URL.Query().Get("dimension") != "stock" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sampleRebalanceStock())

		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/portfolio/targets":
			var payload struct {
				Dimension string                `json:"dimension"`
				Targets   []PortfolioTargetItem `json:"targets"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			res := PortfolioTargets{
				MemberID:          0,
				Dimension:         payload.Dimension,
				Targets:           payload.Targets,
				TargetCashPercent: ptr(10.0),
				Disclaimer:        TargetDisclaimer,
			}
			_ = json.NewEncoder(w).Encode(res)

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/portfolio/targets":
			res := PortfolioTargets{
				MemberID:          0,
				Dimension:         "stock",
				Targets:           []PortfolioTargetItem{{TargetKey: "2330", TargetPercent: 100.0}},
				TargetCashPercent: ptr(0.0),
				Disclaimer:        TargetDisclaimer,
			}
			_ = json.NewEncoder(w).Encode(res)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL, "test-key", time.Second)

	// 1. PortfolioRebalance
	reb, err := client.PortfolioRebalance(context.Background(), RebalanceOptions{
		Dimension:        "stock",
		LotSize:          1000,
		TolerancePercent: 2.0,
	})
	if err != nil {
		t.Fatalf("PortfolioRebalance 失敗: %v", err)
	}
	if reb.TotalPortfolioValue != 6000000.0 {
		t.Errorf("PortfolioRebalance 數值錯誤: %f", reb.TotalPortfolioValue)
	}

	// 2. SetPortfolioTargets
	setRes, err := client.SetPortfolioTargets(context.Background(), SetTargetsOptions{
		Dimension: "stock",
		Targets:   []PortfolioTargetItem{{TargetKey: "2330", TargetPercent: 90.0}},
	})
	if err != nil {
		t.Fatalf("SetPortfolioTargets 失敗: %v", err)
	}
	if len(setRes.Targets) != 1 || setRes.Targets[0].TargetPercent != 90.0 {
		t.Errorf("SetPortfolioTargets 回傳內容錯誤: %+v", setRes)
	}

	// 3. GetPortfolioTargets
	getRes, err := client.GetPortfolioTargets(context.Background(), 0, "stock")
	if err != nil {
		t.Fatalf("GetPortfolioTargets 失敗: %v", err)
	}
	if len(getRes.Targets) != 1 || getRes.Targets[0].TargetKey != "2330" {
		t.Errorf("GetPortfolioTargets 回傳內容錯誤: %+v", getRes)
	}
}

// TestRebalanceExplicitZero 驗證 JSON 明確傳入零時與省略參數有不同語意。
func TestRebalanceExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		body                string
		tolerance, discount float64
	}{
		{`{}`, 2, 1}, {`{"tolerance_percent":0,"fee_discount":0}`, 0, 0},
	} {
		var input RebalanceInput
		if err := json.Unmarshal([]byte(tc.body), &input); err != nil {
			t.Fatal(err)
		}
		fake := &fakeRebalanceQuerier{rebalanceResult: sampleRebalanceStock()}
		tools := portfolioRebalanceToolset{pr: fake, logf: func(string, ...any) {}}
		if _, _, err := tools.getPortfolioRebalance(t.Context(), nil, input); err != nil {
			t.Fatal(err)
		}
		if fake.lastRebalance.TolerancePercent != tc.tolerance || fake.lastRebalance.FeeDiscount != tc.discount {
			t.Fatalf("presence lost: %+v", fake.lastRebalance)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fee_discount") != "0" || r.URL.Query().Get("tolerance_percent") != "0" {
			t.Errorf("zero query lost: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dimension":"stock"}`))
	}))
	defer server.Close()
	if _, err := NewAPIClient(server.URL, "key", time.Second).PortfolioRebalance(t.Context(), RebalanceOptions{LotSize: 1, Dimension: "stock"}); err != nil {
		t.Fatal(err)
	}
}

// TestTargetsDimensionsAndCoverage 驗證各維度現金與 ETF 限制會留在文字與結構資料。
func TestTargetsDimensionsAndCoverage(t *testing.T) {
	var targets PortfolioTargets
	if err := json.Unmarshal([]byte(`{"dimension":"all","target_cash_percent":null,"cash_percent_by_dimension":{"stock":40,"industry":20},"targets":[{"dimension":"stock","target_key":"2330","target_percent":60}]}`), &targets); err != nil {
		t.Fatal(err)
	}
	text := formatTargetsText(&targets, false)
	for _, want := range []string{"stock 2330", "stock 維度現金/未分配比例: 40.00%", "industry 維度現金/未分配比例: 20.00%"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s: %s", want, text)
		}
	}
	r := sampleRebalanceStock()
	r.EtfCoverage = []RebalanceEtfCoverage{{Symbol: "0050", CoveredWeightPercent: 60, UncoveredValue: 4000}}
	r.Warnings = []string{"僅前十大成分"}
	text = formatRebalanceText(r)
	if !strings.Contains(text, "涵蓋率 60.00%") || !strings.Contains(text, "僅前十大成分") {
		t.Fatal(text)
	}
}

// TestTargetsPrecisionAndReplacement 驗證入庫精度、重複鍵及全現金設定。
func TestTargetsPrecisionAndReplacement(t *testing.T) {
	fake := &fakeRebalanceQuerier{targetsResult: &PortfolioTargets{Dimension: "stock"}}
	tools := portfolioRebalanceToolset{pr: fake, logf: func(string, ...any) {}}
	for _, items := range [][]PortfolioTargetItem{
		{{TargetKey: "A", TargetPercent: 33.335}}, {{TargetKey: "A", TargetPercent: 10}, {TargetKey: "A", TargetPercent: 10}}, {{TargetKey: " ", TargetPercent: 1}},
	} {
		if _, _, err := tools.setPortfolioTargets(t.Context(), nil, SetTargetsInput{Dimension: "stock", Targets: items}); err == nil {
			t.Fatalf("accepted invalid input: %+v", items)
		}
	}
	if _, _, err := tools.setPortfolioTargets(t.Context(), nil, SetTargetsInput{Dimension: "stock", Targets: []PortfolioTargetItem{}}); err != nil {
		t.Fatal(err)
	}
}

// TestRebalanceToolErrors 驗證參數邊界與上游失敗均不會回傳假成功。
func TestRebalanceToolErrors(t *testing.T) {
	fake := &fakeRebalanceQuerier{rebalanceResult: sampleRebalanceStock(), targetsResult: &PortfolioTargets{Dimension: "stock"}}
	ts := portfolioRebalanceToolset{pr: fake, logf: func(string, ...any) {}}
	for _, input := range []RebalanceInput{{MemberID: -1}, {Dimension: "bad"}, {LotSize: 2}, {TolerancePercent: ptr(-1.0)}, {TolerancePercent: ptr(math.NaN())}, {AvailableCash: -1}, {AvailableCash: math.Inf(1)}, {FeeDiscount: ptr(2.0)}, {FeeDiscount: ptr(math.NaN())}} {
		if _, _, err := ts.getPortfolioRebalance(t.Context(), nil, input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	for _, input := range []SetTargetsInput{{MemberID: -1, Dimension: "stock"}, {Dimension: "bad"}, {Dimension: "stock", Targets: []PortfolioTargetItem{{TargetKey: "A", TargetPercent: math.NaN()}}}, {Dimension: "stock", Targets: []PortfolioTargetItem{{TargetKey: "A", TargetPercent: 101}}}} {
		if _, _, err := ts.setPortfolioTargets(t.Context(), nil, input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	for _, input := range []GetTargetsInput{{MemberID: -1}, {Dimension: "bad"}} {
		if _, _, err := ts.getPortfolioTargets(t.Context(), nil, input); err == nil {
			t.Fatal(input)
		}
	}
	if _, out, err := ts.getPortfolioTargets(t.Context(), nil, GetTargetsInput{}); err != nil || out.(PortfolioTargetsOutput).DataKind != "portfolio_targets" {
		t.Fatalf("get targets: %v %v", out, err)
	}
	if _, _, err := ts.getPortfolioTargets(t.Context(), nil, GetTargetsInput{Dimension: "industry", MemberID: 12}); err != nil {
		t.Fatal(err)
	}
	fake.err = errors.New("upstream unavailable")
	if _, _, err := ts.getPortfolioRebalance(t.Context(), nil, RebalanceInput{}); err == nil {
		t.Fatal("lost upstream error")
	}
	if _, _, err := ts.setPortfolioTargets(t.Context(), nil, SetTargetsInput{Dimension: "stock"}); err == nil {
		t.Fatal("lost upstream error")
	}
	if _, _, err := ts.getPortfolioTargets(t.Context(), nil, GetTargetsInput{}); err == nil {
		t.Fatal("lost upstream error")
	}
}

// TestRebalanceFormatStates 驗證空持股、零股、ETF 與產業無候選標的的顯示。
func TestRebalanceFormatStates(t *testing.T) {
	r := sampleRebalanceStock()
	r.StockItems = nil
	r.LotSize = 1
	if !strings.Contains(formatRebalanceText(r), "目前無持股") {
		t.Fatal("missing empty holdings")
	}
	r.StockItems = []StockRebalanceItem{{Symbol: "A", Action: "hold"}}
	if !strings.Contains(formatRebalanceText(r), "維持") {
		t.Fatal("missing hold")
	}
	r.Dimension = "industry"
	r.IndustryItems = []IndustryRebalanceItem{{Industry: "其他"}}
	if !strings.Contains(formatRebalanceText(r), "無現有持股") {
		t.Fatal("missing candidate state")
	}
	r.Dimension = "etf_look_through"
	r.EtfItems = []EtfLookThroughRebalanceItem{{StockSymbol: "A", Name: "測試", DirectValue: 10, IndirectValue: 20, TotalValue: 30}}
	if !strings.Contains(formatRebalanceText(r), "ETF穿透曝險與偏離") {
		t.Fatal("missing ETF state")
	}
}

// TestRebalanceClientFailures 驗證 HTTP 與 JSON 錯誤、空陣列正規化與 PUT 錯誤分類。
func TestRebalanceClientFailures(t *testing.T) {
	status, body := 200, `{}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	c := NewAPIClient(server.URL, "key", time.Second)
	opt := RebalanceOptions{MemberID: 3, Dimension: "stock", LotSize: 1}
	if r, err := c.PortfolioRebalance(t.Context(), opt); err != nil || r.StockItems == nil || r.IndustryItems == nil || r.EtfItems == nil {
		t.Fatalf("arrays: %v %v", r, err)
	}
	if r, err := c.SetPortfolioTargets(t.Context(), SetTargetsOptions{}); err != nil || r.Targets == nil {
		t.Fatal(r, err)
	}
	if r, err := c.GetPortfolioTargets(t.Context(), 0, ""); err != nil || r.Targets == nil {
		t.Fatal(r, err)
	}
	status = 404
	if _, err := c.PortfolioRebalance(t.Context(), opt); err == nil {
		t.Fatal("missing no-target error")
	}
	status = 502
	if _, err := c.PortfolioRebalance(t.Context(), opt); err == nil {
		t.Fatal("missing upstream error")
	}
	if _, err := c.GetPortfolioTargets(t.Context(), 1, "stock"); err == nil {
		t.Fatal("missing get error")
	}
	if _, err := c.SetPortfolioTargets(t.Context(), SetTargetsOptions{}); err == nil {
		t.Fatal("missing put status error")
	}
	status = 200
	body = `not json`
	if _, err := c.SetPortfolioTargets(t.Context(), SetTargetsOptions{}); err == nil {
		t.Fatal("missing put parse error")
	}
	if err := c.put(t.Context(), "/x", map[string]float64{"bad": math.NaN()}, nil); err == nil {
		t.Fatal("missing marshal error")
	}
	bad := NewAPIClient("http://[::1", "key", time.Second)
	if err := bad.put(t.Context(), "/x", nil, nil); err == nil {
		t.Fatal("missing URL error")
	}
	server.Close()
	if err := c.put(t.Context(), "/x", nil, nil); err == nil {
		t.Fatal("missing connection error")
	}
}

// TestBacktestBoundaryMetadata 驗證 API 的跨界排除數量不會在 MCP 結構化輸出遺失。
func TestBacktestBoundaryMetadata(t *testing.T) {
	var offset BacktestOffsetResult
	if err := json.Unmarshal([]byte(`{"rebalance_offset":0,"excluded_cross_boundary_periods":2}`), &offset); err != nil {
		t.Fatal(err)
	}
	if offset.ExcludedCrossBoundaryPeriods == nil || *offset.ExcludedCrossBoundaryPeriods != 2 {
		t.Fatal(offset)
	}
	var year BacktestWalkForwardYear
	if err := json.Unmarshal([]byte(`{"year":2024,"excluded_training_periods":1}`), &year); err != nil {
		t.Fatal(err)
	}
	if year.ExcludedTrainingPeriods == nil || *year.ExcludedTrainingPeriods != 1 {
		t.Fatal(year)
	}
	encoded, err := json.Marshal(year)
	if err != nil || !strings.Contains(string(encoded), `"excluded_training_periods":1`) {
		t.Fatal(string(encoded), err)
	}
}

// TestRebalanceMCPRoundTrip 以真實 SDK transport 驗證註冊、參數與結構化輸出契約。
func TestRebalanceMCPRoundTrip(t *testing.T) {
	fake := &fakeRebalanceQuerier{rebalanceResult: sampleRebalanceStock(), targetsResult: &PortfolioTargets{Dimension: "all", CashPercentByDimension: map[string]float64{"stock": 40}}}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	AddTools(server, fake, nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := client.Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	seen := map[string]bool{}
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		seen[tool.Name] = true
	}
	for _, name := range []string{"get_portfolio_rebalance", "set_portfolio_targets", "get_portfolio_targets"} {
		if !seen[name] {
			t.Fatal("unregistered", name)
		}
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"get_portfolio_rebalance", map[string]any{"tolerance_percent": 0, "fee_discount": 0}},
		{"set_portfolio_targets", map[string]any{"dimension": "stock", "targets": []any{}}},
		{"get_portfolio_targets", map[string]any{}},
	} {
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err != nil || result.IsError || result.StructuredContent == nil {
			t.Fatalf("%s: %+v %v", tc.name, result, err)
		}
	}
	if fake.lastRebalance.TolerancePercent != 0 || fake.lastRebalance.FeeDiscount != 0 {
		t.Fatal("MCP changed explicit zero", fake.lastRebalance)
	}
}
