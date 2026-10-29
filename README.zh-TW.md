# stock-mcp-go

[English](README.md) | [繁體中文](README.zh-TW.md)

以 Go 與官方 MCP Go SDK 打造的台股唯讀 Model Context Protocol（MCP）伺服器，透過 stateless Streamable HTTP 提供股票報價、公司基本資料、財務歷史、估值、選股與市場分析工具。

> [!IMPORTANT]
> 本服務大多數資料都是歷史資料或上游資料庫目前最新可取得的資料，不是交易所逐筆行情，也不保證即時。唯一可能回傳即時結果的工具是盤中使用的 `get_market_movers`。請依各工具的來源、日期與免責聲明欄位判讀，實際結構因工具而異。

## 功能特色

- Stateless MCP Streamable HTTP endpoint，所有工具皆標示為唯讀
- 由 `stock_rust` Data API 提供 68 個工具（除 `set_portfolio_targets` 與 `restore_portfolio_target_revision` 外皆唯讀）
- 回傳繁體中文摘要與可供程式處理的 structured content
- 支援多組 MCP API Key 的建立、編輯、啟用、停用、輪替與撤銷
- 使用 HMAC-SHA-256 與 server-side pepper 驗證 API Key，不保存明文 Key
- 內嵌 API Key 管理介面，使用安全的 8 小時 HttpOnly session
- 依 API Key ID 與用戶端 IP 的組合進行流量限制
- Origin 驗證、request body 上限、結構化 log 與 graceful shutdown
- 提供 liveness 與會實際檢查資料來源的 readiness endpoint
- 每次工具呼叫記一筆結構化 log（工具名稱、結果、耗時、API Key 前綴）
- 內嵌 Swagger UI（`/docs`）與 OpenAPI 3.1 文件（`/openapi.json`），由實際註冊的工具產生
- Distroless、non-root 容器映像與內建健康檢查

## 系統架構

```text
MCP client
    │  HTTPS + Authorization: Bearer <MCP_API_KEY>
    ▼
反向代理
    ▼
stock-mcp-go
    ├── /mcp                         stateless MCP endpoint
    ├── /healthz 與 /readyz          健康檢查
    ├── /admin/mcp-api-keys          內嵌管理介面
    ├── web/                          驗證、限流與 HTTP 安全
    ├── apikey/                       SQLite Key store 與記憶體 snapshot
    └── stock/                        MCP tools 與 Data API client
          └── HTTP ─────────────────► stock_rust Data API
```

> [!NOTE]
> 直連 PostgreSQL 的 `db` 模式已於 2026-10 移除，`stock_rust` Data API 是唯一資料來源。設定 `DATA_SOURCE=db` 會拒絕啟動；舊的 `DATABASE_URL`、`DB_*` 變數會被忽略，可以從 `.env` 刪除。

## MCP 工具

| 工具 | 適用模式 | 功能 |
| --- | --- | --- |
| `search_stock` | API | 依股票代號或中英文名稱搜尋 |
| `get_latest_daily_quote` | API | 查詢最新可取得的日報價 |
| `get_price_history` | API | 依日期區間查詢歷史日線；`adjusted=true` 加上除權息／分割還原後的開高低收與累計還原因子 |
| `get_stock_profile` | API | 查詢基本資料、報價、EPS、ROE、每股淨值與歷史高低點 |
| `get_realtime_snapshot` | API | 查詢第三方近即時快照；不保證即時 |
| `get_monthly_revenue_history` | API | 查詢單月與累計營收歷史 |
| `get_financial_statement_history` | API | 查詢季度或年度財務指標 |
| `get_financial_reports` | API | 查詢單季或年度三大財務報表：損益表、資產負債表、現金流量表（金額單位新台幣千元） |
| `get_dividend_fill` | API | 填息分析：除權息後第幾個交易日回到除權息前收盤，附平均／中位數填息天數與未填息差距 |
| `get_financial_health` | API | 以 Piotroski F 分數評估年度財務體質（9 項指標與前一年度比較，附本期數值與比較基準） |
| `get_dividend_history` | API | 查詢歷年現金與股票股利 |
| `get_stock_valuation` | API | 查詢最新或指定日期以前的估值模型結果 |
| `get_valuation_band` | API | 查詢目前本益比與股價淨值比在近 1～10 年歷史中的百分位、最小值／P25／中位數／P75／最大值，以及各分位對應股價 |
| `get_stock_peers` | API | 比較個股與同產業上市櫃股票（不含 ETF）的市值、本益比、股價淨值比、殖利率、F 分數、毛利率與最新月營收年增率，附產業中位數與個股各指標排名 |
| `get_market_breadth` | API | 查詢漲跌家數、52 週新高／新低家數、均線上下家數與估值分布 |
| `get_dividend_yield_ranking` | API | 查詢上市櫃股票歷史殖利率排行 |
| `screen_stocks` | API | 以白名單內的基本面、估值與財務體質條件（F 分數、毛利率、負債比、近四季自由現金流、最新季營益率，以及三率皆較去年同季上升的 `margins_yoy_up`）、最新月營收創歷史新高的 `revenue_record`、排除產業的 `exclude_industry_ids`（如金融業 17）、營收動能（連續年增月數、近 3 月累計年增率）、最新月營收下限（排除低基期）、RS 分數下限、外資／投信連續買超天數下限、近 20 根日線平均成交值下限（`min_avg_trade_value`，單位新台幣百萬元），以及 10 日內最新日線的官方本益比／股價淨值比上限（`max_pe`、`max_pb`；虧損股不通過本益比上限；`min_major_change` 為集保千張大戶持股比例近 `major_change_weeks` 週（預設 4）的增加下限，沒有大戶資料的股票不列入）篩選，可依 `pe_ratio`、`pb_ratio` 排序；每檔附 RS 分數、外資／投信連續買賣超、20 日均成交值與本益比／股價淨值比及日期 |
| `get_market_index_history` | API | 查詢台股加權指數歷史 |
| `get_dividend_calendar` | API | 查詢除權息與股利發放事件 |
| `get_qfii_holding_ranking` | API | 查詢最新外資持股快照排行 |
| `get_market_movers` | API | 查詢漲幅、跌幅或成交量排行，自動選擇盤中或收盤資料 |
| `get_industry_performance` | API | 查詢某交易日或最近 N 個交易日的上市櫃普通股產業表現：市值加權與中位數漲跌幅、上漲／下跌家數、成交值占比、本益比與股價淨值比中位數，以及各產業漲跌幅最高的股票 |
| `get_technical_indicators` | API | 查詢最近 N 個交易日（預設 20）的技術指標：5／10／20／60／120／240 日均線與乖離率、RSI(6/12/14)、KD(9,3,3)、MACD(12,26,9)，附最新日黃金／死亡交叉、收盤穿越月／季／年線與超買／超賣訊號（股價未還原除權息） |
| `get_signal_stocks` | API | 查詢某交易日（預設最新）出現技術訊號的上市櫃股票：MA／KD／MACD 黃金或死亡交叉、收盤穿越月／季／年線、RSI(14)／K 超買超賣、52 週新高低，以及 RS 分數穿越 80／50（`rs80`／`rs50`）；可依市場、訊號類型、指標與產業篩選，依成交值排序並附各訊號家數（收盤後預先計算，股價未還原除權息） |
| `get_signal_performance` | API | 統計區間內各技術訊號後 5／20／60／120／240 個交易日的平均／中位數還原含息報酬、勝率、相對同日市場平均的超額報酬與勝大盤比例，附市場基準；預設近一年、最長 3653 天，超過 1096 天須指定 `indicator`，訊號自 2019 年起。可依市場、訊號類型、指標、產業、訊號當日 RS 分數（`min_rs_score`／`max_rs_score`）與外資／投信連續買超天數下限（`min_foreign_streak`／`min_trust_streak`）篩選；`group_by=year` 加上逐年統計與基準 |
| `get_stock_signal_history` | API | 查詢個股一段期間的技術訊號（預設 180 天、最長 730 天），由新到舊列出當日收盤、漲跌幅與後續 5／20／60／120／240 個交易日的還原報酬 |
| `get_stock_timeline` | API | 個股事件時間軸（預設 90 天、最長 730 天），由新到舊列出除權息與發放日、減資與分割、月營收（法定公告期限）與創歷史新高、季報（EPS、ROE）與三率年增／年減季度（金融業除外）、技術訊號、外資／投信連續買賣超達 5／10／20 天、董監持股或設質月變動至少 1%；另列未來 60 天已公告股利與減資（`upcoming`），附類別統計並支援 `kinds` 篩選 |
| `get_relative_strength_ranking` | API | 上市櫃普通股相對強度（RS）排行：以加權還原報酬（0.4×60 日＋0.3×120 日＋0.3×240 日）計算 1～99 百分位分數，排除 ETF 與特別股，附排名與 20／60／120／240 日報酬；可依市場、產業或單一代號篩選，`order=asc` 查最弱股票 |
| `get_relative_strength_history` | API | 個股最近 N 個交易日的 RS 趨勢（預設 60、最多 120）：每日分數與上市櫃普通股排名（排除 ETF 與特別股，算法同排行）、區間高低、分數變動與穿越 50／80 門檻紀錄 |
| `get_industry_strength` | API | 產業相對強度：各產業有評分股票的 RS 中位數（對照上市櫃普通股，排除 ETF 與特別股）、強勢（RS ≥ 80）與弱勢（RS ≤ 20）占比、平均加權報酬、前三名領漲股，以及相較約 20 個交易日前的中位數變動，用於觀察資金輪動 |
| `get_institutional_flow_ranking` | API | 最近 N 個交易日（1～60）外資、投信、自營商或三大法人合計買賣超排行：淨張數、估計淨金額（每日淨股數×當日收盤）、占期間成交量比例、買賣超天數與目前連續買賣超；可依市場與產業篩選 |
| `get_institutional_streak_performance` | API | 統計法人連續買超或賣超 N 天（1～20、預設 3）後 5／20／60／120／240 個交易日的平均／中位數還原含息報酬、勝率、相對同日市場平均的超額報酬與勝大盤比例，附市場基準；可選外資、投信、自營商或合計、依市場與產業篩選，期間預設近一年、最長 3653 天，籌碼自 2019 年起；`group_by=year` 加上逐年統計與基準 |
| `get_screen_backtest` | API | 選股策略回測（預設近三年、最長 3653 天）：每 `hold_days` 個交易日（5／20／60／120／240，預設 20）依調倉日 RS 範圍、外資／投信連續買超、持有期間內技術訊號、市場與產業條件，從上市櫃普通股選出 RS 最高的 `limit` 檔（預設 20）等權持有。回傳各期報酬與持股、總／年化報酬、最大回撤、勝率、相對同日母體平均與同期 0050 的比較、年化波動與 Sharpe、逐年明細、全期貢獻／拖累股，以及尚未結束的最新一期持股與距下次調倉交易日數。支援 `keep_rank` 排名緩衝、`hold_min_rs_score` 續抱門檻、`max_per_industry` 產業檔數上限（由其他產業遞補）、`max_weight` 個股權重上限（餘額分配其他股票，檔數不足則留現金）、`exclude_industry`（逗號分隔產業名稱）、`rebalance_offset` 調倉位移、`min_avg_trade_value` 新買進股票的 20 日均成交值下限（百萬元）、`rank_by` 改依均成交值／外資投信連買天數／當時已公告營收年增率／季營益率／當日官方本益比或股價淨值比（低到高）排序、`etf_weight` 固定配置 0～100% 的 0050，以及 `weighting` 依均成交值或其平方根加權。市場過濾可用 `market_filter_ma`（加權指數低於 20／60／120／240 日線時持有現金）、`market_filter_breadth`（站上 60 日線股票占比下限）、`market_filter_max_pe`（市場本益比中位數上限）、`market_filter_pe_percentile`（近五年歷史百分位上限，過往不足 250 個交易日視為通過）；`min_major_change`／`major_change_weeks` 為千張大戶持股比例變化條件，只用換股日前已公布的週（資料 2023-07-14 起，`from` 須晚於第一週加回看週數；只用於新買進）。目前持股附權重、新買進標示、最近調倉賣出股票與調倉後各股／加權策略／0050 還原報酬。另列扣除依周轉計算的券商手續費（可設 `fee_discount`）與交易稅後績效，包含續抱股恢復目標權重與 0050 核心恢復固定比重的成本；只使用當時可得資料，下市股計至最後收盤。`stop_loss`／`trailing_stop`（1～50%）依還原日收盤檢查進場價／期間最高收盤價跌幅，`close` 於觸發日收盤賣出、`next_open` 於次一個有開盤價的交易日開盤賣出（已到期末則按期末出場），計入成本並留現金至下次調倉。所有回測工具皆支援 `preset` 載入 stock_rust 已存策略（明確傳入參數優先），以及新買進股票的當時可得基本面篩選：`min_revenue_yoy_percent`、`min_revenue_yoy_streak`、`min_revenue_yoy_3m_percent`（次月 11 日起使用月營收）、`min_eps`、`min_roe_percent`（法定季報期限次日起使用）、`min_gross_margin_percent`、`min_operating_margin_percent`、`margins_yoy_up`（三率皆高於去年同季）、`max_debt_ratio_percent`（Yahoo 季報自 2015 年起），以及遵循相同公告期限的 `revenue_record`、`min_monthly_revenue`（千元）、`positive_free_cash_flow`（近四個已公告季度自由現金流為正），與調倉日官方 `max_pe`／`max_pb` 上限（虧損股不通過本益比上限） 所有回測工具另可選 `execution_price`（`close` 預設；`next_open` 換股日收盤後於次一交易日開盤買賣，支援開盤對開盤日頻回撤與次日開盤停損出場）與 `slippage_bps`（買賣各計 0～200 基點），回應揭露 `execution_model` 與 `slippage_bps`。 |
| `get_screen_backtest_offsets` | API | 回測調倉日穩健度：參數同 `get_screen_backtest`（不含 `rebalance_offset`），逐一測試 0～`hold_days`−1 的調倉位移（期間較長時取 20 個均勻位移），回傳各位移摘要、扣成本年化報酬／Sharpe／最大回撤／波動率的最小值、四分位、中位數、平均與最大值，對照同期 0050，並列報酬／Sharpe 勝過 0050 或回撤較淺的比例，以及逐年扣成本報酬分布與勝過 0050 的比例；中位數比單次回測更具參考性。可用 `split` 日期分開評估樣本內外績效，檢查過度擬合 |
| `get_screen_backtest_frontier` | API | 尋找選股策略搭配 0050 的配置：參數同 `get_screen_backtest`（不含 `rebalance_offset`、`etf_weight`），以 0%、10%…100% 的 0050 核心比重各跑所有調倉位移，回傳各比重扣成本年化報酬／Sharpe／最大回撤／波動率分布、勝過 0050 比例，以及中位數 Sharpe／報酬最高的比重（0% 為純策略，100% 為純 0050）；`split` 加上每種比重的樣本內外分布，以及樣本內 Sharpe 最佳比重 |
| `get_screen_backtest_attribution` | API | 解釋策略超額報酬來源：參數同 `get_screen_backtest`（不含 `etf_weight`，只分析策略部分），以近 20 日均成交值最高的 50 檔並依其加權建立類 0050 基準，依 Brinson-Fachler 分成產業配置、個股選擇與交互作用；列全期、逐年（前五產業）與逐產業結果，附同期 0050 報酬 |
| `get_screen_backtest_walk_forward` | API | 滾動樣本外驗證：參數同 `get_screen_backtest`，另加 `train_years`（1～5，預設 3）；每年只用過去訓練期間，從 36 組參數（0050 核心 0／30／50／70% × 個股上限無／10／20% × keep_rank 為 limit 的 1／1.5／2 倍）選出扣成本 Sharpe 最佳組合，套用當年；回傳逐年選擇、串接的樣本外摘要、固定參數與 0050 對照，以及各調倉位移分布 |
| `compare_screen_backtests` | API | 並排比較 2～4 個回測情境：共用選股條件，各情境可改 `execution_price`、`slippage_bps`、`fee_discount`、`etf_weight`、`max_weight`、`keep_rank` 或 `rebalance_offset`；回傳各情境彙總、依年度結果與相對第一個情境的年化差異。 |
| `list_strategy_presets` | API | 列出 stock_rust 已存的具名策略：名稱、說明、回測查詢參數與是否由 22:10 調倉通知執行；回測工具可用 `preset` 引用 |
| `get_strategy_edge` | API | 查詢策略相對 0050 的最近優勢檢查，每月第一個交易日更新；含檢查日、近期／全期偏移分布與 holds／weakened／unknown 判斷。即時重算請用 `get_screen_backtest_offsets`。僅供參考，不構成投資建議，亦不會自動下單。 |
| `get_strategy_track` | API | 具名策略實際追蹤：讀取每次 22:10 調倉通知當時記錄的持股，逐期列出各股、策略部分與混合 0050 組合的還原報酬，串接累積報酬並對照持有 0050（未扣成本）；每期從調倉到下次調倉，最新一期到最近交易日，記錄只寫一次，不受之後重跑回測或修改策略影響 |
| `get_industry_flow` | API | 最近 N 個交易日（1～60）的產業法人資金流：各產業估計淨金額、占產業成交值比例、買超／賣超股票數與前三名買超／賣超股，用於觀察法人加減碼方向 |
| `get_margin_ranking` | API | 最近 N 個交易日（1～60）融資融券排行：融資或融券增減最多、券資比最高（融資至少 100 張），附餘額、增減與收盤價；可依市場與產業篩選 |
| `get_market_regime` | API | 某交易日（預設最新）的加權指數趨勢：收盤相對 5／20／60／120／240 日均線與斜率、多空／混合排列、連續位於 60 日線同側天數、距 52 週高低點、5／20／60／240 日報酬、近 60 個交易日穿越 20／60／120／240 日線日期；另附市場廣度（上市櫃股票站上 20／60／240 日線占比及五日前對照、近五日 52 週新高低家數）與估值位階（官方本益比／股價淨值比中位數、近五年百分位與五個交易日前本益比） |
| `get_market_valuation` | API | 大盤估值位階：近 1～10 年（預設 5 年）上市櫃每日官方本益比／股價淨值比中位數，只計正值（排除虧損股與 ETF），附最小值／四分位／最大值、最新日在期間內的百分位與月底數值；可選市場（`twse`／`tpex`）與截止日 |
| `get_market_forward_returns` | API | 大盤等權未來報酬：近 1～20 年（預設 3 年）每個基準日買進同市場（全部、上市或上櫃）全部股票，5／20／60／120／240 個交易日後的平均還原報酬與上漲比例，整段與依年度；與 `get_signal_performance` 的大盤基準同一份資料，不等於加權指數 |
| `get_chip_data` | API | 查詢個股籌碼：每日三大法人、融資融券、主力淨進出、連續買賣超、千張大戶與 400 張以上、董監設質與最新日買賣超分點（主力資料僅涵蓋持股） |
| `get_holder_levels` | API | 查詢個股單週集保股權分散全部持股分級：1～15 級（1～999 股到 1,000,001 股以上）、差異數調整與合計的人數、股數與比例；可指定日期（取該日以前最近一週），預設最新一週；分級資料自 2026-10-22 起逐週累積 |
| `get_cagr_ranking` | API | 查詢 M3～Y10 年化報酬率排行（可選只看股價、含股利或股利再投入），可依市場與產業篩選 |
| `get_stock_cagr` | API | 查詢個股各期間的年化報酬率與總報酬 |
| `get_portfolio_summary` | API | 個人持股總覽：市值、成本、未實現損益、已收股利、產業權重、市值加權殖利率／F 分數、組合本益比／股價淨值比（市值加權調和平均，排除虧損股）；每檔附官方本益比／股價淨值比、最新月營收年增率與連續年減月數、最新季三率與年增減方向（金融業除外）、近四季自由現金流，以及基本面警訊（營收連三月以上年減、三率皆降、營業虧損、近四季自由現金流為負），摘要列出有警訊持股；`member_id=0` 查所有成員 |
| `get_portfolio_history` | API | 個人持股近 1～120 個月績效：時間加權報酬（排除買賣資金流，只計股價）、年化報酬、最大回撤、月報酬、加權指數對照，以及依每日報酬計算的 beta、年化 alpha、相關係數、追蹤誤差與波動率 |
| `get_holding_dividend_fill` | API | 個人持股（含 ETF）最近 N 天（預設 365、30～730）的逐次除息填息狀態：填息價（前收）、最新收盤、是否填息與第幾個交易日填息、尚未填息差距（差距最大優先），附整體填息統計 |
| `screen_etfs` | API | 篩選與排序台灣上市櫃 ETF（每天收盤後更新）：分類、投資地區、配息頻率、市場、關鍵字；可依成交值、規模、折溢價、近 12 個月殖利率、淨申購、含息報酬、受益人數排序；每檔附收盤、預估淨值與折溢價、規模、淨申購、配息與下次除息 |
| `get_etf_detail` | API | 單檔 ETF 完整資料：分類、投信、追蹤指數、費率，收盤、預估淨值與折溢價（含近 20 筆平均）、規模、淨申購、受益人數、配息，市價與淨值含息報酬（1 週至 5 年）、近 1 年風險指標、前十大持股、行業比重、資產分佈與近期每日折溢價 |
| `compare_etfs` | API | 並排比較 2 至 4 檔 ETF：經理費與保管費、規模、折溢價（含近 20 筆平均）、殖利率與配息頻率、近 1 年市價與淨值含息報酬、3 年年化報酬、波動、最大回撤、夏普值，以及兩兩前十大持股重疊權重 |
| `get_etf_holding_history` | API | 列出 ETF 最近 1–24 期各期前十大成分股與權重；期別依實際採集日期，非固定季度 |
| `get_etf_proxy_tracking` | API | ETF 相對 0050 的近一年報酬差異與年化波動（至少 120 組樣本；為代理基準，非相對實際指數的追蹤誤差） |
| `analyze_stock_deep_dive` | API | 一次查詢個股完整分析：報價與基本面、五年估值區間、五日籌碼、營收、三率、三大財報、財務體質、技術與股利；缺資料會列警示 |
| `get_etf_holding_changes` | API | 單檔 ETF 最近幾期（預設 6、約每月一期）的前十大持股，每期列出與前一期相比的新進、剔除與權重變化 |
| `get_portfolio_etf_exposure` | API | 個人持股穿透曝險：持有 ETF 依最新一期前十大持股權重拆到成分股，與直接持股合併，列出合計曝險最大的成分股（直接、經由哪些 ETF、占總市值比例）與每檔 ETF 可拆解的比例 |
| `get_portfolio_rebalance` | API | 依已設定的目標權重（個股／產業／ETF 穿透）試算持股再平衡：偏離、建議股數與金額、手續費與證交稅、現金流；僅供參考，不下單 |
| `get_portfolio_targets` | API | 查詢各維度已設定的目標權重與保留現金比例 |
| `set_portfolio_targets` | API | **寫入工具**：儲存單一維度的目標權重（總和不得超過 100%）；僅作再平衡試算基準，不是委託單 |
| `list_portfolio_target_revisions` | API | 列出單一維度目標配置的歷史版本（每次變更前的整組，由新到舊、最多 20 版）與各版現金比例；用來找要還原的版本 |
| `restore_portfolio_target_revision` | API | **寫入工具**：把目標配置還原成某個版本；被取代的那一組會另存成新版本，所以還原可以再復原 |
| `get_holding_chip` | API | 某交易日（預設最新）的個人持股籌碼日報：當日收盤仍持有的普通股（排除 ETF），逐檔列出外資／投信／自營商淨買賣超與占成交量比例、外資／投信連續買賣超天數、融資融券餘額與日變動、主力淨進出及買賣超分點與近五個主力交易日合計；達到每日 Telegram 通知門檻的持股標示原因 |
| `get_dividend_forecast` | API | 目前持股未來 1～24 個月預期現金股利：已公告配息與依去年模式推估、每月總額、逐筆明細與預期殖利率 |

除 `set_portfolio_targets` 與 `restore_portfolio_target_revision`（僅儲存個人目標權重）外，所有工具都是唯讀。回應欄位因工具而異：報價輸出包含 `data_as_of`，其他工具可能以結果物件內的日期或區間表達時間基準。允許空值的缺失指標維持 `null`；空集合與合計為 0 也可能是有效結果。`get_dividend_forecast` 會以逐筆 `basis` 與 `announced_total`／`estimated_total` 區分已公告與推估股利，推估值不代表已公告配息。

## 系統需求

- Go 1.27（依 `go.mod`）
- 可連線的相容 `stock_rust` Data API 與其 Bearer Key（唯一支援的資料來源）
- 正式環境：Caddy、Nginx 等 HTTPS 反向代理

## 快速開始

```bash
git clone https://github.com/jiansoft/stock-mcp-go.git
cd stock-mcp-go
cp .env.example .env
```

至少需要設定：

```dotenv
STOCK_RUST_API_BASE_URL=http://127.0.0.1:9002
STOCK_RUST_API_KEY=replace-with-a-dedicated-upstream-key
MCP_API_KEY=replace-with-an-initial-client-key
MCP_API_KEY_PEPPER=replace-with-at-least-32-random-bytes
MCP_ADMIN_TOKEN=replace-with-a-different-32-byte-random-secret
```

`MCP_API_KEY` 只用於 bootstrap。API Key 的 SQLite 資料庫為空時會匯入一次；確認匯入成功後即可從環境變數移除。

啟動時從目前工作目錄讀取 `.env`，不覆寫既有 OS 環境變數。SQLite 路徑未使用絕對路徑時，也以工作目錄為基準；服務執行帳號必須能寫入該目錄。

啟動服務：

```bash
go run .
```

專案內的 `.env.example` 使用 port `9005`；若未設定 `PORT`，程式預設值則是 `3000`。

確認服務狀態：

```bash
curl http://127.0.0.1:9005/healthz
curl http://127.0.0.1:9005/readyz
```

正常回應為 `{"status":"ok"}`。上游 Data API 無法使用或金鑰失效時，`/readyz` 會回 HTTP 503。

`/healthz` 只確認 HTTP 程序可回應；`/readyz` 以帶驗證的上游股票搜尋（`2330`、limit 1）檢查連線，逾時三秒、結果快取兩秒。通過不代表已驗證全部工具或資料新鮮度。

API 文件：瀏覽器開啟 `http://127.0.0.1:9005/docs`（Swagger UI，不需驗證）。文件由實際註冊的工具產生，`tools/call` 的參數依工具分別列出；Try it out 填入 MCP API Key 即可直接呼叫，頁面會自動補上 MCP 要求的 `Accept: application/json, text/event-stream`。

## MCP Client 設定

預設 endpoint 是 `/mcp`，接受帶有驗證資訊的 `POST` 請求：

```http
Authorization: Bearer <MCP_API_KEY>
```

Claude Code 範例：

```bash
claude mcp add --transport http stock-mcp https://mcp.example.com/mcp \
  --header "Authorization: Bearer <MCP_API_KEY>"
```

本服務使用 stateless Streamable HTTP，不提供 stdio transport、伺服器端 session 或 SSE 訂閱 endpoint；`GET /mcp` 與 `DELETE /mcp` 會回 `405 Method Not Allowed`。

## 環境變數

| 變數 | 預設值 | 說明 |
| --- | --- | --- |
| `APP_ENV` | `development` | `development`、`production` 或 `test` |
| `HOST` | `127.0.0.1` | 監聽 IP 位址（不接受主機名稱）；容器內使用 `0.0.0.0` |
| `PORT` | `3000` | HTTP port（`.env.example` 會覆寫為 `9005`） |
| `MCP_PATH` | `/mcp` | MCP endpoint 路徑 |
| `TRUST_PROXY` | `false` | 是否信任代理附加的用戶端 IP 資訊 |
| `TRUSTED_PROXY_HOPS` | `1` | 會附加 `X-Forwarded-For` 的受信任代理層數 |
| `STOCK_RUST_API_BASE_URL` | 必填 | 上游 Data API base URL |
| `STOCK_RUST_API_KEY` | 必填 | 上游專用 Bearer Key |
| `API_TIMEOUT_MS` | `30000` | 上游 HTTP timeout（如 `get_institutional_streak_performance` 等多年期統計，在 Pi 上可能需要數秒） |
| `MCP_API_KEY` | 空 | 一次性相容 bootstrap Key |
| `MCP_API_KEY_DB_PATH` | `data/mcp-api-keys.db` | API Key SQLite 路徑 |
| `MCP_API_KEY_PEPPER` | 必填 | 至少 32 bytes 的 HMAC pepper |
| `MCP_ADMIN_TOKEN` | 必填 | 至少 32 bytes 的獨立管理密鑰 |
| `MCP_TRUSTED_ORIGINS` | 空 | 逗號分隔的允許瀏覽器 Origin |
| `RATE_LIMIT_WINDOW_MS` | `60000` | Rate limit 視窗 |
| `RATE_LIMIT_MAX_REQUESTS` | `60` | 每組 API Key ID＋來源 IP 在視窗內的請求上限；超額回 HTTP 429 |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error` |

上游 Key、MCP Client Key、pepper 與 admin token 都必須使用不同的秘密。請勿提交 `.env`。

工具呼叫 log：每次 `tools/call` 記一筆 `msg="MCP 工具呼叫"`，欄位為 `tool`、`status`（`ok`、`tool_error`、`protocol_error`）、`duration_ms` 與呼叫端的 `key_prefix`；不記錄參數內容。正式環境建議 `LOG_LEVEL=info`。

## API Key 管理

開啟內嵌管理介面：

```text
https://mcp.example.com/admin/mcp-api-keys
```

第一次使用 `MCP_ADMIN_TOKEN` 登入後，伺服器在記憶體建立有效 8 小時的 session，並回傳 `HttpOnly`、`SameSite=Strict` cookie。請求直接使用 TLS，或同時設定 `TRUST_PROXY=true` 且收到 `X-Forwarded-Proto=https` 時，才會加上 `Secure`。程序重啟後所有管理 session 失效。Token 不會寫入瀏覽器儲存空間。

自動化程式也可以用 `Authorization: Bearer <MCP_ADMIN_TOKEN>` 呼叫管理 API：

| Method | Endpoint | 功能 |
| --- | --- | --- |
| `POST` / `DELETE` | `/api/admin/session` | 登入或登出 |
| `GET` / `POST` | `/api/admin/mcp-api-keys` | 列出或建立 Key |
| `GET` / `PATCH` / `DELETE` | `/api/admin/mcp-api-keys/{id}` | 讀取、更新或撤銷 Key |
| `POST` | `/api/admin/mcp-api-keys/{id}/enable` | 啟用 Key |
| `POST` | `/api/admin/mcp-api-keys/{id}/disable` | 停用 Key |
| `POST` | `/api/admin/mcp-api-keys/{id}/rotate` | 輪替 Key |

完整 API Key 只會在建立或輪替後回傳一次。SQLite 只保存 public prefix 與 `HMAC-SHA-256(pepper, full-key)`，不保存明文。資料庫 transaction commit 後，變更會立刻發布到不可變的記憶體 snapshot。

備份 SQLite Key 資料庫時，也要另外妥善保存配對的 `MCP_API_KEY_PEPPER`。使用不同 pepper 開啟既有資料庫會拒絕啟動，只有資料庫無法還原可用金鑰。複製資料庫前先停止程序，或使用能包含已提交 WAL 資料的 SQLite 備份方式。

API Key store 的設計範圍是單一 Go process 搭配一個本機 SQLite volume。不可讓多個 replica 共用同一個 SQLite 檔案；多 replica 部署需要共享的關聯式 Key store 與跨程序 refresh 機制。

## Docker

從原始碼建置並啟動：

```bash
docker compose -f docker-compose.example.yml up --build
```

Compose 範例會把 `127.0.0.1:9004` 映射到容器 port `3000`，並用 `mcp-api-key-data` named volume 保存 API Key 狀態。映像以 non-root 身分執行；因 distroless runtime 沒有 shell、`curl` 或 `wget`，健康檢查會使用執行檔內建的 `-health-check` 模式。

`STOCK_RUST_API_BASE_URL` 必須設為容器可連到的位址；`127.0.0.1:9002` 指向容器自己。使用 Compose 時，主機反向代理應指向 `127.0.0.1:9004`；下方原生程序範例則使用 `9005`。

`Dockerfile_live` 與 `control.sh` 則提供另一套預先編譯 ARM binary 的部署流程。`build.ps1` 支援 Linux ARM64 與 ARMv7，部署時需把對應的 `stock-mcp_linux_*` binary 放在部署目錄根層。

### 部署到 Raspberry Pi（原生程序）

```powershell
.\scripts\deploy-armv7.ps1 -Build        # 交叉編譯、上傳、control.sh update、驗證
.\scripts\deploy-armv7.ps1 -StageOnly    # 只上傳到 /tmp 並比對 sha256
```

腳本會檢查 ELF 架構、比對上傳後的 sha256，當遠端 `control.sh` 與倉庫版本不同時一併更新（上傳前統一為 LF 換行；`stop` 最多等待 90 秒讓舊程序結束（可用 `STOP_TIMEOUT` 覆寫）；`update` 仍失敗時，腳本會再等舊程序最多 120 秒，結束後重跑一次 `update`，避免服務停著沒人啟動；`start` 在新程序立即退出時回報失敗），再確認程序、埠、`/healthz`、`/readyz` 與啟動 log；失敗時印出回滾指令（`control.sh` 會把舊檔備份成 `<執行檔>.<時間戳>`）。預設目標為 `pi@192.168.111.138:/opt/stock_mcp`、port `9005`。

## 反向代理

正式環境必須使用 HTTPS。最小 Caddy 設定：

```caddyfile
mcp.example.com {
    reverse_proxy 127.0.0.1:9005
}
```

最小 Nginx 設定：

```nginx
location / {
    proxy_pass http://127.0.0.1:9005;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

只有服務確實位於受信任的反向代理後方時，才設定 `TRUST_PROXY=true`。`TRUSTED_PROXY_HOPS` 必須等於會附加 `X-Forwarded-For` 的代理層數：Client → Nginx → Server 使用 `1`；Client → CDN → Nginx → Server 使用 `2`。設得太大可能讓 rate limit 使用的 IP 身分可被偽造。

可以的話，請在網路邊界額外限制管理路由：

- `/admin/`
- `/api/admin/`

## 資料與安全語意

- `get_market_movers` 是唯一 `is_realtime` 可能為 `true` 的工具。
- 台股收盤到最終日線資料產生前，movers 可能回退到前一交易日；回應會標明實際來源與日期。
- `get_realtime_snapshot` 是第三方近即時快照，但刻意標示為不保證即時。
- 估值區間、排行與選股結果只描述歷史模型輸出，不是目標價或交易建議。
- 外資持股是最新快照而非時間序列，無法用來推論增持或減持趨勢。
- MCP request body 上限為 1 MiB；管理 API request body 上限為 64 KiB。
- 驗證失敗請求有獨立限流，通過驗證後才套用一般的每 Key 限流。
- Log 不會記錄秘密或 Authorization header。

## 開發指令

```bash
make test        # go test ./...
make lint        # go vet ./...
make fmt-check   # 檢查 gofmt
make build       # 建置 ./stock-mcp
```

## 已知限制

- 資料新鮮度取決於上游 `stock_rust` 的蒐集與處理排程。
- 工具註冊反映的是 client 實作，並未探測已部署上游的能力。新工具需要對應的 stock_rust endpoint 與資料集，單靠 `/readyz` 無法確認相容性。
- Rate limit 計數與 API Key 驗證 snapshot 都只存在單一 process。
- SQLite API Key repository 只支援單一 process 與本機持久化 volume。
- 本服務只提供 MCP Tools，不提供 Resources、Prompts 或 Sampling。
- 所有輸出僅供資訊參考，不構成投資建議。
