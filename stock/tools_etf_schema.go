// screen_etfs／get_etf_detail／compare_etfs 的輸入 schema。

package stock

import "github.com/google/jsonschema-go/jsonschema"

// compareEtfsSchema 描述 compare_etfs 的輸入形狀。
func compareEtfsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"symbols"},
		Properties: map[string]*jsonschema.Schema{
			"symbols": {
				Type: "array", MinItems: ptr(2), MaxItems: ptr(4),
				Items:       &jsonschema.Schema{Type: "string", MinLength: ptr(1), MaxLength: ptr(12)},
				Description: "要比較的 ETF 代號,2 至 4 檔,例如 [\"0050\", \"006208\"]",
			},
		},
	}
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
