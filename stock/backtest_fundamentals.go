package stock

import (
	"fmt"
	"math"

	"github.com/google/jsonschema-go/jsonschema"
)

// 回測的基本面條件:月營收與季報,只用換股當天已公布的資料(Data API 依法定公布期限判斷)。

// BacktestFundamentals 是回測的基本面門檻;nil、0(連續月數)與 false(三率三升、營收新高、自由現金流)代表不篩選。
type BacktestFundamentals struct {
	MinRevenueYoYPercent      *float64
	MinRevenueYoYStreak       int
	MinRevenueYoY3mPercent    *float64
	MinEPS                    *float64
	MinROEPercent             *float64
	MinGrossMarginPercent     *float64
	MinOperatingMarginPercent *float64
	MarginsYoYUp              bool
	MaxDebtRatioPercent       *float64
	RevenueRecord             bool
	MinMonthlyRevenue         *float64
	PositiveFreeCashFlow      bool
	MaxPE                     *float64
	MaxPB                     *float64
	// MinMajorChange 是換股前已公布的集保千張大戶持股比例,最近一週相對 MajorChangeWeeks 週前的變化下限(百分點);
	// nil 為不篩選,週數 0 代表沿用 Data API 預設的 4 週。
	MinMajorChange   *float64
	MajorChangeWeeks int
	// MinHolder400Change 是 400 張以上大戶持股比例的同樣變化下限(百分點,回看週數同 MajorChangeWeeks);nil 為不篩選。
	MinHolder400Change *float64
	// MaxMarginChangePercent 是融資餘額最近一筆對往前第 20 筆的變化率上限(%;換股日只用前一個交易日以前的資料);nil 為不篩選。
	MaxMarginChangePercent *float64
	// MaxPEG 是 PEG(換股當天本益比 ÷ 換股日已公布最近一季的近四季 EPS 成長率)上限;nil 為不篩選。
	MaxPEG *float64
	// MaxVolatilityPercent、MaxBeta 是換股當天近 240 筆日線的年化波動度(%)與對加權指數 beta 上限;nil 為不篩選。
	MaxVolatilityPercent *float64
	MaxBeta              *float64
	// MinEPSYoYPercent、MinAnnualEPSGrowthPercent、NewHighDays 是 CANSLIM 的 C、A、N:已公布的最近一季單季 EPS
	// 年增率下限、已公布的近 3 年每年 EPS 年增率下限,以及換股日往前幾個日曆天內創過 52 週新高(0 為不篩)。
	MinEPSYoYPercent          *float64
	MinAnnualEPSGrowthPercent *float64
	NewHighDays               int
	// MinFScore 是換股當天已公布的最近一個年度 Piotroski F 分數下限(0 至 9);nil 為不篩選。
	MinFScore *int
	// MinDividendYieldPercent、MinFillRatePercent、MaxAvgFillDays、MinDividendYears 是存股條件:換股當天的現金殖利率下限、
	// 近 5 年填息率下限、平均填息天數上限與連續配息年數下限(nil 與 0 為不篩),只用換股當天已發生的除息。
	MinDividendYieldPercent *float64
	MinFillRatePercent      *float64
	MaxAvgFillDays          *float64
	MinDividendYears        int
	// MinRevenueAccelerationMonths 是營收加速月數下限:已公布的月營收從最新月往回,年增率逐月高於前一個月(月份相連)
	// 的月數(1 至 11);0 為不篩選。
	MinRevenueAccelerationMonths int
	// MinDividendGrowthYears 是股利成長年數下限:換股年以前的完整年度現金股利合計,從換股年前一年往回每年不低於
	// 前一年(年度相連)的年數(1 至 30);0 為不篩選。
	MinDividendGrowthYears int
	// TrendTemplate 為 true 時只選換股當天符合 Minervini 趨勢模板(MA20 > MA60 > MA120 > MA240、收盤站上 MA240、不低於 52 週最高價的 75% 且比 52 週最低價高 30% 以上)的股票。
	TrendTemplate bool
}

// backtestFundamentalSchemas 回傳基本面門檻參數的 schema,加進回測類工具的輸入。
func backtestFundamentalSchemas() map[string]*jsonschema.Schema {
	revenueRule := "月營收法定次月 10 日前公布,換股日只用已公布的月份(次月 11 日起),最近一筆距換股日超過 3 個月視為沒有資料;只用於新買進,續抱不看基本面"
	reportRule := "季報以法定期限隔天起才用(Q1 5/16、Q2 8/15、Q3 11/15、Q4 隔年 4/1),距換股日超過 3 季不用;財報從 2018 年起,為目前留存的數字;只用於新買進"
	statementRule := "用 Yahoo 單季財報(2015 年起),公布日規則同 min_eps(法定期限隔天起才用、距換股日超過 3 季不用);沒有資料的股票不通過;只用於新買進"
	return map[string]*jsonschema.Schema{
		"min_revenue_yoy_percent": {
			Type: "number", Minimum: ptr(-100.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近一個月營收年增率至少這麼高(%,選填,-100 至 10000)。" + revenueRule,
		},
		"min_revenue_yoy_streak": {
			Type: "integer", Minimum: ptr(1.0), Maximum: ptr(12.0),
			Description: "換股當天已公布的月營收從最近一個月往回連續年增(>0、月份相連)至少這麼多個月(選填,1 至 12)。" + revenueRule,
		},
		"min_revenue_yoy_3m_percent": {
			Type: "number", Minimum: ptr(-100.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近 3 個月(須相連)合計營收年增率至少這麼高(%,選填,-100 至 10000)。" + revenueRule,
		},
		"min_eps": {
			Type: "number", Minimum: ptr(-10000.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近一季每股盈餘至少這麼高(元,選填,-10000 至 10000)。" + reportRule,
		},
		"min_roe_percent": {
			Type: "number", Minimum: ptr(-10000.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近一季股東權益報酬率至少這麼高(%,選填,-10000 至 10000)。" + reportRule,
		},
		"min_gross_margin_percent": {
			Type: "number", Minimum: ptr(-10000.0), Maximum: ptr(100.0),
			Description: "換股當天已公布的最近一季單季毛利率至少這麼高(%,選填,-10000 至 100)。" + statementRule,
		},
		"min_operating_margin_percent": {
			Type: "number", Minimum: ptr(-10000.0), Maximum: ptr(100.0),
			Description: "換股當天已公布的最近一季單季營業利益率至少這麼高(%,選填,-10000 至 100)。" + statementRule,
		},
		"margins_yoy_up": {
			Type:        "boolean",
			Description: "true 時只選三率三升:換股當天已公布的最近一季毛利率、營業利益率、稅後淨利率都高於去年同一季(去年同季沒有資料的不通過)。" + statementRule,
		},
		"max_debt_ratio_percent": {
			Type: "number", Minimum: ptr(0.0), Maximum: ptr(1000.0),
			Description: "換股當天已公布的最近一季季末負債比不超過這個值(%,選填,0 至 1000)。" + statementRule,
		},
		"trend_template": {
			Type: "boolean",
			Description: "true 時只選換股當天收盤後符合 Minervini 趨勢模板的股票:MA20 > MA60 > MA120 > MA240、收盤站上 MA240、不低於 52 週最高價的 75% 且比 52 週最低價高 30% 以上;" +
				"年線或 52 週最低價為 0(上市未滿一年或資料不足)的不通過。只用於新買進,可搭配 min_rs_score 找相對強勢股",
		},
		"revenue_record": {
			Type:        "boolean",
			Description: "true 時只選換股當天已公布的最新月營收創歷史新高(高於之前所有月份,之前至少 12 個月)的股票。" + revenueRule,
		},
		"min_monthly_revenue": {
			Type: "number", Minimum: ptr(0.0), Maximum: ptr(1e12),
			Description: "換股當天已公布的最近一個月營收至少這麼多(新台幣千元,選填,0 至 1000000000000;例如 100000 為 1 億元)。" +
				"用來排除營收規模太小、年增率或營業利益率因低基期失真的股票(依 rank_by=revenue_yoy、operating_margin 排序時特別有用)。" + revenueRule,
		},
		"positive_free_cash_flow": {
			Type: "boolean",
			Description: "true 時只選換股當天已公布的近四季自由現金流合計為正的股票:最近一季" + statementRule +
				";連同之前 3 季須四季都有資料",
		},
		"max_pe": {
			Type: "number", Minimum: ptr(0.0), Maximum: ptr(10000.0),
			Description: "換股當天的官方本益比(證交所/櫃買中心日資料)不超過這個值(倍,選填,0 至 10000)。" +
				"本益比為 0(近四季虧損或沒有資料)的股票不通過;收盤後公布,與以收盤價買進同一時點;只用於新買進",
		},
		"max_pb": {
			Type: "number", Minimum: ptr(0.0), Maximum: ptr(1000.0),
			Description: "換股當天的官方股價淨值比不超過這個值(倍,選填,0 至 1000);為 0(沒有資料)的股票不通過,其餘同 max_pe",
		},
		"min_major_change": {
			Type: "number", Minimum: ptr(-100.0), Maximum: ptr(100.0),
			Description: "集保千張大戶持股比例的變化下限(百分點,選填,-100 至 100;例如 1 代表近 major_change_weeks 週增加至少 1 個百分點):" +
				"取換股日之前已公布的最近一週(集保週五資料週六才公布,換股當天看不到),減去它往前第 major_change_weeks 週;任一週沒有該股資料的不通過。" +
				"大戶資料 2023-07-14 起,from 必須晚於 2023-07-14 加 major_change_weeks 週;只用於新買進,續抱不看",
		},
		"major_change_weeks": {
			Type: "integer", Minimum: ptr(1.0), Maximum: ptr(12.0),
			Description: "min_major_change 與 min_holder_400_change 的回看週數(集保資料週,選填,1 至 12,預設 4);兩者都沒設時不影響結果",
		},
		"min_holder_400_change": {
			Type: "number", Minimum: ptr(-100.0), Maximum: ptr(100.0),
			Description: "集保 400 張以上大戶持股比例的變化下限(百分點,選填,-100 至 100;籌碼集中):規則同 min_major_change," +
				"取換股日之前已公布的最近一週減去往前第 major_change_weeks 週;大戶資料 2023-07-14 起,from 必須晚於 2023-07-14 加回看週數。只用於新買進",
		},
		"max_margin_change_percent": {
			Type: "number", Minimum: ptr(-100.0), Maximum: ptr(1000.0),
			Description: "融資餘額變化率上限(%,選填,-100 至 1000;籌碼集中,例如 -5 代表近一個月融資至少減少 5%):融資餘額最近一筆對同一檔往前第 20 筆," +
				"融資收盤後才公布,換股日只用前一個交易日以前的資料;基期融資為 0(不能融資或沒有資料)的不通過。只用於新買進",
		},
		"max_peg": {
			Type: "number", Minimum: ptr(0.01), Maximum: ptr(10.0),
			Description: "PEG 上限(0.01 至 10,選填;GARP):本益比 ÷ 近四季 EPS 成長率(%,近四季 Yahoo 單季 EPS 合計對前四季合計,須 8 季相連、前四季合計 > 0);虧損或成長率不在 0～100%(超過 100% 多為低基期轉盈,PEG 失真)的不通過,例如 1 代表本益比不高於成長率。換股日只用已過法定期限的那一季,只用於新買進",
		},
		"max_volatility_percent": {
			Type: "number", Minimum: ptr(1.0), Maximum: ptr(200.0),
			Description: "年化波動度上限(%,1 至 200,選填;低波動):近 240 筆日線漲跌幅的樣本標準差 × √240,樣本不足 200 筆的不通過(0050 約 20～30%)。用換股當天收盤後的指標,只用於新買進",
		},
		"max_beta": {
			Type: "number", Minimum: ptr(-5.0), Maximum: ptr(5.0),
			Description: "beta 上限(-5 至 5,選填;低波動):近 240 筆日線對加權指數日報酬的迴歸斜率;台股大盤由台積電主導,非電子股的 beta 常接近 0。用換股當天收盤後的指標,只用於新買進",
		},
		"min_eps_yoy_percent": {
			Type: "number", Minimum: ptr(-100.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近一季單季 EPS 對去年同季的成長率至少這麼高(%,選填,-100 至 10000;CANSLIM 的 C)。" +
				"去年同季 EPS ≤ 0 的不通過。" + reportRule,
		},
		"min_annual_eps_growth_percent": {
			Type: "number", Minimum: ptr(-100.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近 4 個年度 EPS 算出 3 個年增率,最低的一年至少這麼高(%,選填,-100 至 10000;CANSLIM 的 A)。" +
				"年度 EPS 隔年 4/1 起才用;年度不連續、任一基期 ≤ 0 或最新年度落後換股年超過 2 年的不通過;只用於新買進",
		},
		"new_high_days": {
			Type: "integer", Minimum: ptr(1.0), Maximum: ptr(365.0),
			Description: "只選換股日(含)往前 N 個日曆天內(選填,1 至 365)創過 52 週新高的股票(CANSLIM 的 N;訊號 2019 年起);只用於新買進",
		},
		"min_f_score": {
			Type: "integer", Minimum: ptr(0.0), Maximum: ptr(9.0),
			Description: "換股當天已公布的最近一個年度 Piotroski F 分數至少這麼高(選填,0 至 9;7 以上常見於價值選股)。" +
				"每個年度與前一年度比較,年報隔年 4/1 起才用,評分年度落後換股年超過 2 年或沒有年報的不通過;" +
				"可搭配 max_pb 做低淨值比的價值股;只用於新買進",
		},
		"min_revenue_acceleration_months": {
			Type: "integer", Minimum: ptr(1.0), Maximum: ptr(11.0),
			Description: "營收加速:換股當天已公布的月營收從最新一個月往回,年增率逐月高於前一個月(月份相連)至少這麼多個月(選填,1 至 11;" +
				"例如 2 代表最近 3 個月的年增率一個比一個高)。可搭配 min_revenue_yoy_percent 排除年增率仍為負的股票。" + revenueRule,
		},
		"min_dividend_yield_percent": {
			Type: "number", Minimum: ptr(0.0), Maximum: ptr(100.0),
			Description: "換股當天的現金殖利率至少這麼高(%,選填,0 至 100;存股常用 5):最近一次除息往回 350 天的現金股利合計 ÷ 換股日收盤," +
				"季配、月配會算到一年份;最近一次除息早於換股日 400 天以上(已停止配息)或換股日沒有收盤的不通過。" + dividendRule,
		},
		"min_fill_rate_percent": {
			Type: "number", Minimum: ptr(0.0), Maximum: ptr(100.0),
			Description: "近 5 年填息率至少這麼高(%,選填,0 至 100):除息日在換股日前 5 年內、且換股日前已觀察滿 120 個交易日的現金除息中," +
				"120 個交易日內收盤回到填息價的比例;沒有觀察期已滿的除息不通過。" + dividendRule,
		},
		"max_avg_fill_days": {
			Type: "number", Minimum: ptr(1.0), Maximum: ptr(120.0),
			Description: "近 5 年已填息者的平均填息天數不超過這個值(交易日,選填,1 至 120);事件規則同 min_fill_rate_percent,都沒有填息的不通過。" + dividendRule,
		},
		"min_dividend_years": {
			Type: "integer", Minimum: ptr(1.0), Maximum: ptr(30.0),
			Description: "連續配發現金股利至少這麼多年(選填,1 至 30):以除息日年度計,從換股日以前最近一次除息的年度往回數;" +
				"最近一次除息的年度早於換股年前一年的不通過。" + dividendRule,
		},
		"min_dividend_growth_years": {
			Type: "integer", Minimum: ptr(1.0), Maximum: ptr(30.0),
			Description: "股利成長(股利貴族,選填,1 至 30):以除息日年度加總現金股利,只看換股年以前的完整年度;從換股年前一年往回," +
				"每年現金股利都不低於前一年(年度相連)至少這麼多年。例如 5 代表連續 6 年配息且 5 次都沒減少;換股年前一年沒配息的不通過。" + dividendRule,
		},
	}
}

// dividendRule 是存股條件的共同說明。
const dividendRule = "除息資料來自每日重算的填息事件(日線 2012 年起),只用換股當天已發生的除息;只用於新買進"

// validate 檢查基本面門檻的範圍(與 Data API 相同)。
func (f BacktestFundamentals) validate() error {
	for _, bound := range []struct {
		name     string
		value    *float64
		min, max float64
	}{
		{"min_revenue_yoy_percent", f.MinRevenueYoYPercent, -100, 10000},
		{"min_revenue_yoy_3m_percent", f.MinRevenueYoY3mPercent, -100, 10000},
		{"min_eps", f.MinEPS, -10000, 10000},
		{"min_roe_percent", f.MinROEPercent, -10000, 10000},
		{"min_gross_margin_percent", f.MinGrossMarginPercent, -10000, 100},
		{"min_operating_margin_percent", f.MinOperatingMarginPercent, -10000, 100},
		{"max_debt_ratio_percent", f.MaxDebtRatioPercent, 0, 1000},
		{"min_monthly_revenue", f.MinMonthlyRevenue, 0, 1e12},
		{"max_pe", f.MaxPE, 0, 10000},
		{"max_pb", f.MaxPB, 0, 1000},
		{"min_major_change", f.MinMajorChange, -100, 100},
		{"min_holder_400_change", f.MinHolder400Change, -100, 100},
		{"max_margin_change_percent", f.MaxMarginChangePercent, -100, 1000},
		{"max_peg", f.MaxPEG, 0.01, 10},
		{"max_volatility_percent", f.MaxVolatilityPercent, 1, 200},
		{"max_beta", f.MaxBeta, -5, 5},
		{"min_eps_yoy_percent", f.MinEPSYoYPercent, -100, 10000},
		{"min_annual_eps_growth_percent", f.MinAnnualEPSGrowthPercent, -100, 10000},
		{"min_dividend_yield_percent", f.MinDividendYieldPercent, 0, 100},
		{"min_fill_rate_percent", f.MinFillRatePercent, 0, 100},
		{"max_avg_fill_days", f.MaxAvgFillDays, 1, 120},
	} {
		if v := bound.value; v != nil && (math.IsNaN(*v) || *v < bound.min || *v > bound.max) {
			return fmt.Errorf("參數 %s 必須介於 %s 到 %s 之間", bound.name, formatFloat(bound.min), formatFloat(bound.max))
		}
	}
	if f.MinRevenueYoYStreak != 0 && (f.MinRevenueYoYStreak < 1 || f.MinRevenueYoYStreak > 12) {
		return fmt.Errorf("參數 min_revenue_yoy_streak 必須介於 1 到 12 之間")
	}
	if f.MajorChangeWeeks != 0 && (f.MajorChangeWeeks < 1 || f.MajorChangeWeeks > 12) {
		return fmt.Errorf("參數 major_change_weeks 必須介於 1 到 12 之間")
	}
	if f.NewHighDays != 0 && (f.NewHighDays < 1 || f.NewHighDays > 365) {
		return fmt.Errorf("參數 new_high_days 必須介於 1 到 365 之間")
	}
	if f.MinFScore != nil && (*f.MinFScore < 0 || *f.MinFScore > 9) {
		return fmt.Errorf("參數 min_f_score 必須介於 0 到 9 之間")
	}
	if f.MinRevenueAccelerationMonths != 0 && (f.MinRevenueAccelerationMonths < 1 || f.MinRevenueAccelerationMonths > 11) {
		return fmt.Errorf("參數 min_revenue_acceleration_months 必須介於 1 到 11 之間")
	}
	if f.MinDividendGrowthYears != 0 && (f.MinDividendGrowthYears < 1 || f.MinDividendGrowthYears > 30) {
		return fmt.Errorf("參數 min_dividend_growth_years 必須介於 1 到 30 之間")
	}
	if f.MinDividendYears != 0 && (f.MinDividendYears < 1 || f.MinDividendYears > 30) {
		return fmt.Errorf("參數 min_dividend_years 必須介於 1 到 30 之間")
	}
	return nil
}

// set 把有設定的門檻加進 query string。
func (f BacktestFundamentals) set(values interface{ Set(key, value string) }) {
	for key, value := range map[string]*float64{
		"min_revenue_yoy_percent": f.MinRevenueYoYPercent, "min_revenue_yoy_3m_percent": f.MinRevenueYoY3mPercent,
		"min_eps": f.MinEPS, "min_roe_percent": f.MinROEPercent,
		"min_gross_margin_percent": f.MinGrossMarginPercent, "min_operating_margin_percent": f.MinOperatingMarginPercent,
		"max_debt_ratio_percent": f.MaxDebtRatioPercent, "min_monthly_revenue": f.MinMonthlyRevenue,
		"max_pe": f.MaxPE, "max_pb": f.MaxPB, "min_major_change": f.MinMajorChange,
		"min_holder_400_change": f.MinHolder400Change, "max_margin_change_percent": f.MaxMarginChangePercent,
		"max_peg":                f.MaxPEG,
		"max_volatility_percent": f.MaxVolatilityPercent, "max_beta": f.MaxBeta,
		"min_eps_yoy_percent": f.MinEPSYoYPercent, "min_annual_eps_growth_percent": f.MinAnnualEPSGrowthPercent,
		"min_dividend_yield_percent": f.MinDividendYieldPercent, "min_fill_rate_percent": f.MinFillRatePercent,
		"max_avg_fill_days": f.MaxAvgFillDays,
	} {
		if value != nil {
			values.Set(key, formatFloat(*value))
		}
	}
	if f.MinRevenueYoYStreak > 0 {
		values.Set("min_revenue_yoy_streak", fmt.Sprint(f.MinRevenueYoYStreak))
	}
	if f.MajorChangeWeeks > 0 {
		values.Set("major_change_weeks", fmt.Sprint(f.MajorChangeWeeks))
	}
	if f.NewHighDays > 0 {
		values.Set("new_high_days", fmt.Sprint(f.NewHighDays))
	}
	if f.MinFScore != nil {
		values.Set("min_f_score", fmt.Sprint(*f.MinFScore))
	}
	if f.MinDividendYears > 0 {
		values.Set("min_dividend_years", fmt.Sprint(f.MinDividendYears))
	}
	if f.MinRevenueAccelerationMonths > 0 {
		values.Set("min_revenue_acceleration_months", fmt.Sprint(f.MinRevenueAccelerationMonths))
	}
	if f.MinDividendGrowthYears > 0 {
		values.Set("min_dividend_growth_years", fmt.Sprint(f.MinDividendGrowthYears))
	}
	if f.MarginsYoYUp {
		values.Set("margins_yoy_up", "true")
	}
	if f.RevenueRecord {
		values.Set("revenue_record", "true")
	}
	if f.TrendTemplate {
		values.Set("trend_template", "true")
	}
	if f.PositiveFreeCashFlow {
		values.Set("positive_free_cash_flow", "true")
	}
}

// revenueScaleCashTexts 描述月營收下限與近四季自由現金流條件;nil 與 false 代表不篩選。
func revenueScaleCashTexts(minMonthlyRevenue *float64, positiveFreeCashFlow bool) []string {
	var filters []string
	if minMonthlyRevenue != nil {
		filters = append(filters, "已公布的最新月營收 ≥ "+thousandsToHundredMillion(minMonthlyRevenue))
	}
	if positiveFreeCashFlow {
		filters = append(filters, "已公布的近四季自由現金流為正")
	}
	return filters
}

// valuationCapTexts 描述換股當天本益比與股價淨值比上限;nil 代表不篩選。
func valuationCapTexts(maxPE, maxPB *float64) []string {
	var filters []string
	if maxPE != nil {
		filters = append(filters, "換股當天本益比 ≤ "+formatFloat(*maxPE)+" 倍(虧損不選)")
	}
	if maxPB != nil {
		filters = append(filters, "換股當天股價淨值比 ≤ "+formatFloat(*maxPB)+" 倍")
	}
	return filters
}

// lowVolatilityTexts 描述年化波動度與 beta 上限;nil 代表不篩選。
func lowVolatilityTexts(maxVolatility, maxBeta *float64) []string {
	var filters []string
	if maxVolatility != nil {
		filters = append(filters, "近 240 日年化波動度 ≤ "+formatFloat(*maxVolatility)+"%")
	}
	if maxBeta != nil {
		filters = append(filters, "beta(對加權指數)≤ "+formatFloat(*maxBeta))
	}
	return filters
}

// pegTexts 描述 PEG 上限;nil 代表不篩選。
func pegTexts(maxPEG *float64) []string {
	if maxPEG == nil {
		return nil
	}
	return []string{"PEG(本益比 ÷ 近四季 EPS 成長率)≤ " + formatFloat(*maxPEG) + "(虧損、衰退或成長逾 100% 不選)"}
}

// defaultMajorChangeWeeks 是 Data API 的 major_change_weeks 預設值。
const defaultMajorChangeWeeks = 4

// majorChangeTexts 描述千張大戶持股比例變化條件;minChange 為 nil 代表不篩選,週數 0 為預設 4 週。
func majorChangeTexts(minChange *float64, weeks int) []string {
	if minChange == nil {
		return nil
	}
	if weeks <= 0 {
		weeks = defaultMajorChangeWeeks
	}
	return []string{fmt.Sprintf("千張大戶持股比例近 %d 週變化 ≥ %s 個百分點(換股前已公布的週)", weeks, formatFloat(*minChange))}
}

// chipConcentrationTexts 描述籌碼集中條件(400 張大戶比例變化下限、融資餘額變化上限);nil 代表不篩選,週數 0 為預設 4 週。
func chipConcentrationTexts(holder400 *float64, weeks int, margin *float64) []string {
	var filters []string
	if holder400 != nil {
		if weeks <= 0 {
			weeks = defaultMajorChangeWeeks
		}
		filters = append(filters, fmt.Sprintf("400 張大戶持股比例近 %d 週變化 ≥ %s 個百分點(換股前已公布的週)", weeks, formatFloat(*holder400)))
	}
	if margin != nil {
		filters = append(filters, fmt.Sprintf("融資餘額近 20 筆變化 ≤ %s%%(換股日前一個交易日以前)", formatFloat(*margin)))
	}
	return filters
}

// canslimTexts 描述 CANSLIM 的 C、A、N 條件;nil 與 0 代表不篩選。
func canslimTexts(epsYoY, annualGrowth *float64, newHighDays int) []string {
	var filters []string
	if epsYoY != nil {
		filters = append(filters, fmt.Sprintf("已公布的最近一季單季 EPS 年增 ≥ %s%%", formatFloat(*epsYoY)))
	}
	if annualGrowth != nil {
		filters = append(filters, fmt.Sprintf("已公布的近 3 年每年 EPS 年增 ≥ %s%%", formatFloat(*annualGrowth)))
	}
	if newHighDays > 0 {
		filters = append(filters, fmt.Sprintf("近 %d 天內創 52 週新高", newHighDays))
	}
	return filters
}

// fScoreTexts 描述已公布年度 F 分數下限;nil 代表不篩選。
func fScoreTexts(minFScore *int) []string {
	if minFScore == nil {
		return nil
	}
	return []string{fmt.Sprintf("已公布年度 F 分數 ≥ %d", *minFScore)}
}

// revenueAccelerationTexts 描述營收加速條件;0 代表不篩選。
func revenueAccelerationTexts(months int) []string {
	if months <= 0 {
		return nil
	}
	return []string{fmt.Sprintf("已公布的月營收年增率連續 %d 個月逐月提高", months)}
}

// dividendGrowthTexts 描述股利成長條件;0 代表不篩選。
func dividendGrowthTexts(years int) []string {
	if years <= 0 {
		return nil
	}
	return []string{fmt.Sprintf("完整年度現金股利連續 %d 年不減少", years)}
}

// trendTemplateText 是 Minervini 趨勢模板條件的說明。
const trendTemplateText = "符合 Minervini 趨勢模板(MA20 > MA60 > MA120 > MA240、收盤站上 MA240、不低於 52 週最高價的 75% 且比 52 週最低價高 30% 以上)"

// trendTemplateTexts 描述趨勢模板條件;false 代表不篩選。
func trendTemplateTexts(enabled bool) []string {
	if !enabled {
		return nil
	}
	return []string{trendTemplateText}
}

// dividendTexts 描述存股條件(殖利率、填息率、平均填息天數、連續配息年數);nil 與 0 代表不篩選。
func dividendTexts(yield, fillRate, avgFillDays *float64, years int) []string {
	var filters []string
	if yield != nil {
		filters = append(filters, fmt.Sprintf("換股當天現金殖利率 ≥ %s%%", formatFloat(*yield)))
	}
	if fillRate != nil {
		filters = append(filters, fmt.Sprintf("近 5 年填息率 ≥ %s%%", formatFloat(*fillRate)))
	}
	if avgFillDays != nil {
		filters = append(filters, fmt.Sprintf("近 5 年平均填息 ≤ %s 個交易日", formatFloat(*avgFillDays)))
	}
	if years > 0 {
		filters = append(filters, fmt.Sprintf("連續配息 ≥ %d 年", years))
	}
	return filters
}

// revenueRecordTexts 描述營收創歷史新高條件;false 代表不篩選。
func revenueRecordTexts(record bool) []string {
	if !record {
		return nil
	}
	return []string{"已公布的最新月營收創歷史新高"}
}

// fundamentalFilterTexts 描述有設定的基本面門檻;連續月數為 nil 或 0 代表不篩選。
func fundamentalFilterTexts(revenueYoY *float64, streak *int, revenue3m, eps, roe *float64) []string {
	var filters []string
	if revenueYoY != nil {
		filters = append(filters, fmt.Sprintf("已公布的最近月營收年增 ≥ %s%%", formatFloat(*revenueYoY)))
	}
	if streak != nil && *streak > 0 {
		filters = append(filters, fmt.Sprintf("月營收連續年增 ≥ %d 個月", *streak))
	}
	if revenue3m != nil {
		filters = append(filters, fmt.Sprintf("近 3 個月營收年增 ≥ %s%%", formatFloat(*revenue3m)))
	}
	if eps != nil {
		filters = append(filters, fmt.Sprintf("已公布的最近一季 EPS ≥ %s 元", formatFloat(*eps)))
	}
	if roe != nil {
		filters = append(filters, fmt.Sprintf("最近一季 ROE ≥ %s%%", formatFloat(*roe)))
	}
	return filters
}

// marginFilterTexts 描述有設定的財報三率與負債比門檻;三率三升為 false 代表不篩選。
func marginFilterTexts(gross, operating *float64, yoyUp bool, debt *float64) []string {
	var filters []string
	if gross != nil {
		filters = append(filters, fmt.Sprintf("最近一季毛利率 ≥ %s%%", formatFloat(*gross)))
	}
	if operating != nil {
		filters = append(filters, fmt.Sprintf("最近一季營業利益率 ≥ %s%%", formatFloat(*operating)))
	}
	if yoyUp {
		filters = append(filters, "三率三升(毛利率、營業利益率、淨利率都高於去年同季)")
	}
	if debt != nil {
		filters = append(filters, fmt.Sprintf("最近一季負債比 ≤ %s%%", formatFloat(*debt)))
	}
	return filters
}
