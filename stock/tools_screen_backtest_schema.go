// get_screen_backtest 的輸入 schema,以及把等於 schema 預設值的參數清成未指定(引用具名策略時用)。

package stock

import (
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// screenBacktestSchema 描述 get_screen_backtest 的輸入形狀。
func screenBacktestSchema() *jsonschema.Schema {
	kind, indicator := signalFilterSchemas("選換股前持有天數內出現過")
	holdDays := make([]any, 0, len(backtestHoldDays))
	for _, days := range backtestHoldDays {
		holdDays = append(holdDays, days)
	}
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"preset": {
				Type:        "string",
				MaxLength:   ptr(maxPresetNameChars),
				Description: "具名策略(選填,名稱見 list_strategy_presets):以存好的整組條件回測,另外指定的參數會覆蓋存的值(例如 preset=最佳組合、rebalance_offset=3);指定 preset 時 hold_days、limit、market 不套預設值,沿用存的條件;與預設值相同的參數(hold_days=20、limit=20、rank_by=rs_score、weighting=equal、fee_discount=1、market=all)視為沒有指定",
			},
			"from": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "回測起日(格式 YYYY-MM-DD,選填);未提供為迄日往前 1,095 天(約三年)",
			},
			"to": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "回測迄日(格式 YYYY-MM-DD,選填);未提供為最新交易日。起訖最長 3,653 天(約十年)",
			},
			"hold_days": {
				Type:        "integer",
				Enum:        holdDays,
				Default:     []byte("20"),
				Description: "持有天數(交易日,預設 20):每隔這麼多個交易日換股一次,收盤買進、持有到下一次換股",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100.0),
				Default:     []byte("20"),
				Description: "每期最多持有幾檔(預設 20,1 至 100):符合條件者依 rank_by(預設相對強度分數)由高到低取前 N 檔,等權持有",
			},
			"rank_by": {
				Type:        "string",
				Enum:        []any{"rs_score", "trade_value", "foreign_streak", "trust_streak", "revenue_yoy", "operating_margin", "pe_ratio", "pb_ratio"},
				Default:     []byte(`"rs_score"`),
				Description: "符合條件者依什麼排序取前 limit 檔(選填,預設 rs_score):rs_score 相對強度分數;trade_value 近 20 個交易日平均成交金額(偏向大型股);foreign_streak／trust_streak 外資／投信連續買超天數;revenue_yoy 換股日已公布的最新月營收年增率;operating_margin 換股日已公布的最近一季營業利益率;pe_ratio／pb_ratio 換股當天官方本益比／股價淨值比由低到高(虧損或沒有資料的排最後)。同值或查不到時再依 RS 排序(查不到的排最後),keep_rank 的名次也依此排序",
			},
			"etf_weight": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Maximum:     ptr(100.0),
				Default:     []byte("0"),
				Description: "0050 核心部位(選填,0 至 100%,預設 0):組合固定以這個比例持有 0050、其餘才依條件選股(核心／衛星配置),彙總與每期報酬都是混合後的組合報酬;每期調回固定比例的買賣計入成本(0050 證交稅 0.1%,第一次買進 0050 不計),大盤濾網空手時 0050 照常持有。用來看策略搭配大盤能否勝過純 0050",
			},
			"weighting": {
				Type:        "string",
				Enum:        []any{"equal", "trade_value", "sqrt_trade_value"},
				Default:     []byte(`"equal"`),
				Description: "策略部位的持股加權方式(選填,預設 equal):equal 等權;trade_value 依換股日前 20 個交易日平均成交金額加權(接近 0050 的市值加權,偏重大型股);sqrt_trade_value 依平均成交金額的平方根加權(介於兩者之間)。換手成本含續抱股票漲跌後調回目標權重的買賣,目前持股會附各檔權重",
			},
			"max_weight": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100.0),
				Description: "單檔權重上限(選填,1 至 100%):策略部位中每檔最多占這個比例,超出的部分依權重分給其他持股;檔數 × 上限不到 100%(例如上限 10% 只選到 5 檔)時剩下的放現金。依成交金額加權時用來避免集中在台積電等少數大型股,看成績是不是只靠單一權值股",
			},
			"stop_loss": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Description: "停損(選填,1 至 50%):持有期間每天以還原收盤檢查,收盤相對買進價跌幅達這個比例就觸發停損；close 當天收盤賣出，next_open 下一個有開盤的交易日賣出（到達原定出場日則沿用期末出場，不跨期停損），到下次換股前放現金(賣出付手續費與證交稅,下次仍入選視為重新買進);只檢查已滿持有天數的期數,目前持股不檢查",
			},
			"trailing_stop": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Description: "移動停利(選填,1 至 50%):持有期間收盤相對這期最高收盤(含買進價)回落達這個比例就賣出,規則同 stop_loss;兩者都設時先觸發的為準",
			},
			"keep_rank": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(200.0),
				Description: "換股緩衝(選填,需介於 limit 至 200;預設等於 limit 即不緩衝):上一期持有的股票只要這期仍符合條件、且排在前 keep_rank 名就續抱,空出的名額才依 RS 補滿,用來降低換手與交易成本。例如 limit=20、keep_rank=40",
			},
			"hold_min_rs_score": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(99.0),
				Description: "續抱條件(選填,1 至 99):上一期持有的股票即使不再符合選股條件(例如法人連買中斷、訊號過期),只要換股當天 RS 分數不低於此值就續抱;名額不足時先留符合選股條件的股票。用來把進場條件與續抱條件分開、降低換手",
			},
			"max_per_industry": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100.0),
				Description: "產業上限(選填,1 至 100):每期持股(含續抱)同一產業最多幾檔,超過的跳過、改由其他產業依 RS 補滿,用來降低單一產業集中。例如 limit=20、max_per_industry=4",
			},
			"rebalance_offset": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Maximum:     ptr(239.0),
				Default:     []byte("0"),
				Description: "換股日偏移(選填,0 至 hold_days-1,預設 0):起日之後先跳過幾個交易日才第一次換股。同一組條件換不同偏移重跑,可看績效是否只是碰巧遇上某幾個換股日",
			},
			"min_avg_trade_value": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(100000.0),
				Description: "流動性門檻(選填,百萬元,1 至 100000):換股日含當天往前 20 個交易日平均成交金額低於此值的股票不新買進(上一期持股的續抱不受限),用來排除實際買不到量的小型股。例如 50 代表日均成交 5,000 萬元",
			},
			"market_filter_breadth": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(99.0),
				Description: "市場廣度濾網(選填,1 至 99):換股日同市場站上 60 日均線的股票比例低於此百分比時這期空手、賣光上一期持股;可與 market_filter_ma 併用(兩者都通過才持股)。統計從 2019 年起",
			},
			"market_filter_max_pe": {
				Type:        "number",
				Minimum:     ptr(5.0),
				Maximum:     ptr(100.0),
				Description: "大盤估值濾網(選填,5 至 100 倍):換股日同市場官方本益比中位數(get_market_valuation 的每日值)高於此值時這期空手、賣光上一期持股;可與 market_filter_ma、market_filter_breadth 併用(都通過才持股)。資料從 2012 年起,當天沒有估值資料視為通過",
			},
			"market_filter_pe_percentile": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(99.0),
				Description: "相對估值濾網(選填,1 至 99):換股日同市場本益比中位數在換股日之前近 5 年(不含當天)的每日中位數中達第 N 百分位時這期空手、賣光上一期持股;只用當天以前的資料,門檻會跟著市場本益比長期水位移動。之前不足 250 個交易日或沒有估值資料視為通過;可與 market_filter_max_pe 等濾網併用",
			},
			"market_filter_ma": {
				Type:        "integer",
				Enum:        []any{20, 60, 120, 240},
				Description: "大盤濾網(選填):換股日加權指數收盤低於自己的 N 日均線(20/60/120/240 個交易日)時這期空手、賣光持股;不設定為不濾",
			},
			"market": listedOTCMarketSchema(),
			"industry": {
				Type:        "string",
				MaxLength:   ptr(20),
				Description: "只選這個產業的股票(選填),需為完整產業名稱,例如「半導體業」;大盤基準不受影響",
			},
			"exclude_industry": {
				Type:        "string",
				MaxLength:   ptr(200),
				Description: "排除的產業(選填):完整產業名稱,多個以逗號分隔、最多 10 個,例如「金融保險業,航運業」;這些產業的股票不買進,大盤基準不受影響,不可與 industry 相同",
			},
			"min_rs_score":       rsScoreBoundSchema("換股當天相對強度(RS)分數不低於此值(選填,1 至 99)"),
			"max_rs_score":       rsScoreBoundSchema("換股當天相對強度(RS)分數不高於此值(選填,1 至 99)"),
			"min_foreign_streak": backtestStreakSchema("外資(含外資自營商)"),
			"min_trust_streak":   backtestStreakSchema("投信"),
			"kind":               kind,
			"indicator":          indicator,
			"fee_discount": {
				Type:        "number",
				Minimum:     ptr(0.0),
				Maximum:     ptr(1.0),
				Default:     []byte("1"),
				Description: "手續費折扣(選填,0 至 1,預設 1 為不打折;例如 0.28 為 2.8 折)。扣成本後的報酬以手續費 0.1425% 乘上折扣、賣出另加證交稅 0.3% 計算",
			},
			"execution_price": {
				Type:        "string",
				Enum:        []any{"close", "next_open"},
				Default:     []byte(`"close"`),
				Description: "成交價模型(選填):close 為換股日收盤買賣(理想模型,預設);next_open 為換股日收盤後才確定選股、次一交易日開盤買進並在持有期滿後的次一交易日開盤賣出(較貼近實際可成交),開盤價缺漏時沿用收盤。next_open 的停損與移動停利以收盤觸發，次一有開盤的交易日賣出；若已到原定出場日則沿用期末出場，不跨期停損。日頻最大回撤以進場日開盤為起點、出場日開盤結算",
			},
			"slippage_bps": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Maximum:     ptr(200.0),
				Default:     []byte("0"),
				Description: "滑價(選填,基點,0 至 200,預設 0):每次買進、賣出各再扣成交金額的 slippage_bps / 10000(例如 20 代表單邊 0.20%),一併計入扣成本後報酬",
			},
		},
	}
	for name, property := range backtestFundamentalSchemas() {
		schema.Properties[name] = property
	}
	return schema
}

// backtestStreakSchema 描述換股當天法人連續買超天數下限參數。
func backtestStreakSchema(investor string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "integer",
		Minimum:     ptr(1.0),
		Maximum:     ptr(float64(maxStreakDays)),
		Description: "只選換股當天" + investor + "已連續買超至少這麼多天的股票(選填,1 至 60),算法同 get_institutional_flow_ranking",
	}
}

// withoutSchemaDefaults 把等於 schema 預設值的參數清成未指定:MCP SDK 會把 schema 的 default 填進沒給的
// 參數,引用具名策略時這些值會蓋掉策略存的條件。代價是無法用預設值本身覆蓋策略(例如策略存 limit=10
// 時傳 limit=20 會被當成沒給)。
func withoutSchemaDefaults(in ScreenBacktestInput) ScreenBacktestInput {
	if in.HoldDays == 20 {
		in.HoldDays = 0
	}
	if in.Limit == 20 {
		in.Limit = 0
	}
	if in.RankBy == "rs_score" {
		in.RankBy = ""
	}
	if in.Weighting == "equal" {
		in.Weighting = ""
	}
	if in.FeeDiscount != nil && *in.FeeDiscount == 1 {
		in.FeeDiscount = nil
	}
	if strings.TrimSpace(in.Market) == "all" {
		in.Market = ""
	}
	return in
}
