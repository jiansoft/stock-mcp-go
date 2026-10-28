# stock-mcp-go

[English](README.md) | [繁體中文](README.zh-TW.md)

A read-only Model Context Protocol (MCP) server for Taiwan stock data, built with Go and the official MCP Go SDK. It exposes stock quotes, company fundamentals, financial history, valuation, screening, and market analytics over stateless Streamable HTTP.

> [!IMPORTANT]
> Most data returned by this server is historical or the latest data available from the upstream database. It is not exchange-grade tick data and must not be treated as guaranteed real-time market data. The only tool that may return real-time results is `get_market_movers` during market hours. Check each tool's source, date, and disclaimer fields; their exact structure varies by tool.

## Features

- Stateless MCP Streamable HTTP endpoint with read-only tool annotations
- 87 tools (all read-only except `set_portfolio_targets`) backed by the `stock_rust` Data API
- Traditional Chinese text summaries plus structured content for programmatic use
- Multiple MCP API keys with create, edit, enable, disable, rotate, and revoke workflows
- HMAC-SHA-256 API-key verification with a server-side pepper; plaintext keys are never stored
- Embedded administration UI with secure, eight-hour HttpOnly sessions
- Rate limiting by the combination of API-key ID and client IP
- Origin validation, request-size limits, structured logging, and graceful shutdown
- Liveness and data-source-aware readiness endpoints
- One structured log entry per tool call (tool, result, duration, API-key prefix)
- Embedded Swagger UI (`/docs`) and an OpenAPI 3.1 document (`/openapi.json`) generated from the registered tools
- Distroless, non-root container image with a built-in health check

## Architecture

```text
MCP client
    │  HTTPS + Authorization: Bearer <MCP_API_KEY>
    ▼
Reverse proxy
    ▼
stock-mcp-go
    ├── /mcp                         stateless MCP endpoint
    ├── /healthz and /readyz         health endpoints
    ├── /admin/mcp-api-keys          embedded administration UI
    ├── web/                          auth, rate limiting, HTTP security
    ├── apikey/                       SQLite key store and in-memory snapshot
    └── stock/                        MCP tools and Data API client
          └── HTTP ─────────────────► stock_rust Data API
```

> [!NOTE]
> Direct PostgreSQL access (`db` mode) was removed in October 2026; the `stock_rust` Data API is the only data source. `DATA_SOURCE=db` is rejected at startup, while leftover `DATABASE_URL` and `DB_*` variables are ignored and can be deleted.

## MCP tools

`analyze_stock_deep_dive` combines the profile and nine historical analysis sections in one call.
It checks that the symbol exists, uses at most four concurrent requests and a 25-second overall deadline,
and retains available sections with safe warnings when another section fails.

`get_etf_holding_history` returns the full top-ten holdings for 1–24 recorded periods, alongside
the existing change summary tool. `get_etf_proxy_tracking` reports annualized return-difference volatility
and cumulative return differences against **0050 as an ETF proxy**. It requires 120 matching return intervals;
missing data stays null, and these values are not tracking errors against the fund's actual target index.
The Rust deployment must include `etc/sql/etf_tracking_error.sql` and run the ETF performance calculation first.

Monetary summaries use one formatter with explicit source units (TWD, thousand TWD or million TWD),
displaying 元／萬／億 with signs preserved. Per-share prices retain their original precision;
structured data retains the source units and values. Tool descriptions are shorter, while input schemas
continue to describe ranges, units and validation rules.

| Tool | Availability | Purpose |
| --- | --- | --- |
| `search_stock` | API | Search by stock symbol or Chinese/English name |
| `get_latest_daily_quote` | API | Get the latest available daily quote |
| `get_price_history` | API | Query historical daily prices by date range; `adjusted=true` adds dividend/split-adjusted OHLC and the cumulative factor |
| `get_stock_profile` | API | Get company details, quote, EPS, ROE, book value, and price extremes |
| `get_realtime_snapshot` | API | Get a third-party near-real-time snapshot; not guaranteed real-time |
| `get_monthly_revenue_history` | API | Query monthly and cumulative revenue history |
| `get_financial_statement_history` | API | Query quarterly or annual financial metrics |
| `get_financial_reports` | API | Query quarterly or annual income statements, balance sheets, and cash flow statements (amounts in thousand TWD) |
| `get_dividend_fill` | API | Ex-dividend gap fill: how many trading days the price took to recover the pre-ex close, with averages and the gap for unfilled events |
| `get_financial_health` | API | Score annual financial health with the Piotroski F-score (9 criteria vs. the prior year, each with its value and baseline) |
| `get_dividend_history` | API | Query historical cash and stock dividends |
| `get_stock_valuation` | API | Get the latest or date-relative valuation model result |
| `get_valuation_band` | API | Where the current P/E and P/B sit in their own history (1-10 years): percentile, min/P25/median/P75/max, and the implied price at each quartile |
| `get_stock_peers` | API | Compare a stock with its industry peers (listed/OTC, excluding ETFs): market cap, P/E, P/B, dividend yield, F-score, gross margin, and latest monthly revenue YoY, with the industry median and the stock's rank for each metric |
| `get_market_breadth` | API | Get advances/declines, 52-week new highs/lows, moving-average breadth, and valuation distribution |
| `get_dividend_yield_ranking` | API | Rank TWSE/TPEX stocks by historical dividend yield |
| `screen_stocks` | API | Screen stocks with an allowlisted set of fundamental, valuation, financial-health (Piotroski F-score, gross margin, debt ratio, trailing free cash flow, operating margin from the latest quarterly income statement, and `margins_yoy_up` for stocks whose gross, operating and net margins all beat the same quarter last year), `revenue_record` for stocks whose latest monthly revenue is an all-time high, `exclude_industry_ids` to drop industries such as financials (17), revenue-momentum filters (consecutive months of YoY growth, trailing 3-month YoY), a minimum latest monthly revenue to exclude low-base distortions, a minimum relative-strength (RS) score, minimum consecutive net-buy days by foreign investors or investment trusts, a minimum average trade value over the latest 20 daily bars (`min_avg_trade_value`, in NT$ millions), and caps on the official P/E and P/B ratios from the latest daily bar within 10 days (`max_pe`, `max_pb`; loss-making stocks fail the P/E cap), sortable by `pe_ratio` or `pb_ratio`; each result carries its RS score, foreign/trust buy-sell streaks, 20-day average trade value and P/E, P/B with their date |
| `get_market_index_history` | API | Query TAIEX index history |
| `get_dividend_calendar` | API | Query ex-dividend, ex-rights, and dividend-payment events |
| `get_qfii_holding_ranking` | API | Rank the latest QFII holding snapshot |
| `get_market_movers` | API | Rank daily gainers, losers, or volume; automatically selects intraday or closing data |
| `get_industry_performance` | API | Industry performance for a trading day or the last N trading days (listed/OTC common stocks): cap-weighted and median change, advancers/decliners, trading value share, median P/E and P/B, and each industry's top gainer and loser |
| `get_technical_indicators` | API | Technical indicators for the last N trading days (default 20): 5/10/20/60/120/240-day moving averages and bias, RSI(6/12/14), KD(9,3,3), MACD(12,26,9), plus the latest day's golden/death crosses, close crossing the monthly/quarterly/yearly MA, and overbought/oversold signals (prices not adjusted for dividends) |
| `get_signal_stocks` | API | Listed/OTC stocks showing a technical signal on a trading day (default latest): MA/KD/MACD golden or death crosses, close crossing the monthly/quarterly/yearly MA, RSI(14)/K overbought or oversold, 52-week highs/lows, and relative-strength (RS) scores crossing above or below 80/50 (`rs80` / `rs50`); filter by market, signal kind, indicator and industry, sorted by trade value with per-signal counts (signals precomputed after the close; prices not adjusted for dividends) |
| `get_signal_performance` | API | How each technical signal performed over a period (default past year, up to 3,653 days; longer than 1,096 days requires `indicator`; signals start in 2019): average/median adjusted total return 5, 20, 60, 120 and 240 trading days later, win rate, average excess return over the same-day market average and beat-the-market rate, with a market benchmark; filter by market, signal kind, indicator, industry, the relative-strength (RS) score on the signal day (`min_rs_score` / `max_rs_score`), and a minimum foreign or investment-trust consecutive net-buy streak on the signal day (`min_foreign_streak` / `min_trust_streak`); `group_by=year` adds per-year statistics and benchmarks |
| `get_stock_signal_history` | API | Technical signals a stock showed over a period (default 180 days, up to 730), newest first, each with that day's close and change plus the adjusted return 5, 20, 60, 120 and 240 trading days later |
| `get_stock_timeline` | API | A stock's event timeline over a period (default 90 days, up to 730), newest first: ex-dividend/ex-rights and payment dates, capital reductions and splits, monthly revenue (statutory publication deadline) and record-high monthly revenue, quarterly reports (EPS, ROE) and quarters where all three margins rose or fell year over year (not for financials), technical signals, foreign/investment-trust buy or sell streaks reaching 5/10/20 days, and monthly insider holding or pledge changes of at least 1%; also lists announced dividends and reductions in the next 60 days (`upcoming`), with per-kind counts and an optional `kinds` filter |
| `get_relative_strength_ranking` | API | Relative strength (RS) ranking of listed/OTC stocks: percentile score 1–99 of the weighted adjusted return (0.4×60d + 0.3×120d + 0.3×240d) across listed/OTC common stocks (excluding ETFs and preferred shares), with rank and 20/60/120/240-day returns; filter by market, industry or a single symbol, `order=asc` for the weakest |
| `get_relative_strength_history` | API | A stock's relative-strength (RS) trend over the last N trading days (default 60, up to 120): daily score and rank against listed/OTC common stocks (excluding ETFs and preferred shares; same method as the ranking), period high/low, score change and crossings of the 50/80 thresholds |
| `get_industry_strength` | API | Relative strength by industry: median RS score of each industry's scored stocks (scores against listed/OTC common stocks, excluding ETFs and preferred shares), share of strong (RS ≥ 80) and weak (RS ≤ 20) stocks, average weighted return, top-3 leaders and the change in median versus about 20 trading days earlier — shows where money is rotating |
| `get_institutional_flow_ranking` | API | Net buy/sell ranking of foreign investors, investment trusts, dealers or all three over the last N trading days (1–60): net lots, estimated net amount (daily net shares × that day's close), share of period volume, buy/sell days and the current consecutive buy/sell streak; filter by market and industry |
| `get_institutional_streak_performance` | API | How stocks performed after foreign investors, investment trusts, dealers or all three bought (or sold) for N consecutive days (1–20, default 3) over a period (default past year, up to 3,653 days; chip data starts in 2019): average/median adjusted total return 5, 20, 60, 120 and 240 trading days later, win rate, average excess return over the same-day market average and beat-the-market rate, with a market benchmark; filter by market and industry; `group_by=year` adds per-year statistics and benchmarks |
| `get_screen_backtest` | API | Backtest a screening strategy over a period (default past three years, up to 3,653 days): every `hold_days` trading days (5, 20, 60, 120 or 240; default 20) pick listed/OTC common stocks matching the conditions and hold the top `limit` (default 20) by relative-strength score equally weighted; conditions are the RS score range on the rebalance day, a minimum foreign or investment-trust consecutive net-buy streak, a technical signal within the holding window, market and industry. Returns each period's return and holdings, total and annualized return, max drawdown (after-cost figures come in two bases: `daily_max_drawdown_after_costs` from daily NAV and `period_end_max_drawdown_after_costs` from rebalance-end values only) and win rate against the same-day universe average and against holding 0050 over the same periods, annualized volatility and Sharpe ratio, a per-year breakdown, an optional `keep_rank` buffer that keeps last period's holdings while they stay within the top N to cut turnover, an optional `market_filter_ma` that stays in cash when TAIEX closes below its 20/60/120/240-day average, an optional `hold_min_rs_score` that keeps last period's holdings while their RS stays above the threshold even if they no longer pass the entry conditions, an optional `max_per_industry` that caps holdings per industry (skipped names are replaced by the next-ranked stocks from other industries), an optional `market_filter_breadth` that stays in cash when the share of stocks above their 60-day average falls below N%, an optional `market_filter_max_pe` that stays in cash when the market's median official P/E on the rebalance day exceeds N, an optional `market_filter_pe_percentile` that stays in cash when that median ranks at or above the Nth percentile of the previous five years (point-in-time; treated as passing with fewer than 250 prior trading days), the stocks that contributed and dragged the most over the whole backtest, the current holdings from the latest rebalance whose holding period has not ended yet (with trading days until the next rebalance), an optional `rebalance_offset` that shifts the rebalance schedule by N trading days to test whether results depend on the rebalance dates, an optional `min_avg_trade_value` liquidity floor (NT$ millions of 20-day average trade value; new buys only), an optional `rank_by` that ranks qualifying stocks by 20-day average trade value, foreign/trust consecutive net-buy days, the latest published monthly revenue YoY, the latest published quarterly operating margin, or the rebalance day's official P/E or P/B (lowest first) instead of RS score, an optional `etf_weight` that holds a fixed share (0–100%) of 0050 as a core position with the rest in the strategy (returns are for the blended portfolio), an optional `weighting` that weights the strategy's holdings by 20-day average trade value or its square root instead of equally (closer to 0050's cap weighting; current holdings list each weight, mark new buys, list the stocks sold at the latest rebalance, and show the adjusted return since the rebalance close for each holding, the weighted strategy and 0050), an optional `exclude_industry` (comma-separated industry names) whose stocks are never bought, an optional `max_weight` that caps each holding at N% of the strategy (the excess goes to the other holdings; cash when there are too few), plus the same figures after turnover-based brokerage fees (optional `fee_discount`) and transaction tax, including rebalancing kept holdings back to their target weights and the 0050 core back to its fixed share (point-in-time data only; stocks delisted during a period count up to their last close) Optional `stop_loss` / `trailing_stop` (1–50%) sell a holding once its adjusted daily close falls that far from its entry price or from the period high (sold at the trigger day's close for `close`, or at the next trading day with a valid open for `next_open`, falling back to the period-end exit if reached; sale costs charged, cash until the next rebalance). Every backtest tool also accepts `preset` to run a named strategy saved in stock_rust, with explicitly passed parameters overriding the saved ones, and point-in-time fundamental filters for new buys: `min_revenue_yoy_percent`, `min_revenue_yoy_streak`, `min_revenue_yoy_3m_percent` (monthly revenue used from the 11th of the following month) and `min_eps`, `min_roe_percent` (quarterly reports used from the day after the statutory deadline), plus `min_gross_margin_percent`, `min_operating_margin_percent`, `margins_yoy_up` (gross, operating and net margins all above the same quarter last year) and `max_debt_ratio_percent` from Yahoo quarterly statements since 2015, plus `revenue_record` (latest published monthly revenue at an all-time high), `min_monthly_revenue` (latest published monthly revenue floor in thousand NTD, to drop tiny-base outliers) and `positive_free_cash_flow` (trailing four published quarters of free cash flow above zero) under the same deadline rule, plus `max_pe` / `max_pb` caps on the official P/E and P/B ratios of the rebalance day (loss-making stocks fail the P/E cap). Optional `execution_price` (`close` default, or `next_open`: trade at the next trading day's open after the rebalance close, with open-to-open daily drawdown and next-open stop execution) and `slippage_bps` (0–200 basis points per buy and sell) apply to every backtest tool, and responses report `execution_model` and `slippage_bps`. |
| `get_screen_backtest_offsets` | API | Robustness check for a screening backtest: takes the same parameters as `get_screen_backtest` (except `rebalance_offset`) and reruns it for every rebalance offset 0 to `hold_days` − 1 (20 evenly spaced offsets when `hold_days` is longer), returning each offset's summary plus the distribution (min, quartiles, median, mean, max) of after-cost annualized return, Sharpe ratio, max drawdown and volatility alongside 0050 over the same dates, and the share of offsets that beat 0050 on return and on Sharpe or had a shallower drawdown, plus a per-year distribution of after-cost returns against 0050 with the share of offsets beating it each year; the median is a better guide to a strategy than any single backtest An optional `split` date evaluates the periods before and after it separately (in-sample vs out-of-sample) to check for overfitting. |
| `get_screen_backtest_frontier` | API | Finds how much 0050 to pair with a screening strategy: takes the same parameters as `get_screen_backtest` (except `rebalance_offset` and `etf_weight`), blends the strategy with a 0%, 10%, …, 100% 0050 core position and runs every rebalance offset for each, returning the per-weight distribution of after-cost annualized return, Sharpe ratio, max drawdown and volatility with the share of offsets beating 0050, plus the weights with the highest median Sharpe ratio and median return (0% is the pure strategy, 100% is 0050 alone) An optional `split` date adds in-sample and out-of-sample distributions for every weight plus the weight with the best in-sample Sharpe ratio. |
| `get_screen_backtest_attribution` | API | Explains why a screening strategy beat or lagged the market: takes the same parameters as `get_screen_backtest` (except `etf_weight`; strategy sleeve only) and splits its excess return over a 0050-like benchmark (the 50 stocks with the highest 20-day average trade value, weighted by it) into industry allocation, stock selection and interaction (Brinson-Fachler), for the whole period, per year (top five industries) and per industry, with the same-period 0050 return for reference. |
| `get_screen_backtest_walk_forward` | API | Walk-forward check for overfitting: takes the same parameters as `get_screen_backtest` plus `train_years` (1–5, default 3); each year picks the best after-cost Sharpe ratio among 36 parameter sets (0050 core 0/30/50/70% × per-stock cap none/10/20% × keep_rank 1/1.5/2 × limit) using only the previous `train_years` years, applies it to that year, and returns the yearly picks, the chained out-of-sample summary versus the unchanged parameters and 0050, and the distribution across rebalance offsets. |
| `compare_screen_backtests` | API | Compare 2–4 backtest scenarios side by side: shared screening conditions plus per-scenario overrides of `execution_price`, `slippage_bps`, `fee_discount`, `etf_weight`, `max_weight`, `keep_rank` or `rebalance_offset`; returns each scenario's summary and yearly results, with the annualized difference against the first scenario. |
| `list_strategy_presets` | API | Lists the named strategies saved in stock_rust (name, description, backtest query and whether the 22:10 rebalance alert runs it); backtest tools reference them with `preset`. |
| `get_strategy_edge` | API | Cached strategy edge checks versus 0050, updated on the first trading day of each month. Includes check dates, recent/full offset distributions and holds/weakened/unknown verdicts; use `get_screen_backtest_offsets` for fresh calculations. Reference only; no automatic orders. |
| `get_strategy_track` | API | Live tracking of a named strategy: the holdings recorded when each 22:10 rebalance alert was sent, period by period (rebalance → next rebalance, the last one up to the latest trading day) with each holding's adjusted return, the strategy sleeve and the 0050-blended portfolio, chained into a cumulative return versus holding 0050 (before costs); records are written once, so later backtest reruns or preset edits never change them |
| `get_industry_flow` | API | Institutional money flow by industry over the last N trading days (1–60): estimated net amount per industry, share of the industry's trade value, number of net-buy and net-sell stocks, and the top-3 buyers and sellers — shows where institutions are adding or cutting |
| `get_margin_ranking` | API | Margin-trading ranking over the last N trading days (1–60): largest increases or decreases in margin purchases or short sales, or the highest short-to-margin ratio (margin of at least 100 lots), with balances, changes and closing price; filter by market and industry |
| `get_market_regime` | API | TAIEX trend state on a trading day (default latest): close versus the 5/20/60/120/240-day moving averages and their slopes, bullish/bearish/mixed alignment, consecutive days on the same side of the 60-day average, distance from the 52-week high and low, 5/20/60/240-day returns, and the dates in the last 60 trading days when the close crossed above or below the 20/60/120/240-day averages, plus market breadth (share of listed/OTC stocks above their 20/60/240-day averages, versus five days earlier, and 52-week highs/lows over the last five days) and the valuation level (median official P/E and P/B of listed/OTC stocks with their five-year percentiles and the P/E five trading days earlier) |
| `get_market_valuation` | API | Market-wide valuation level: daily medians of the official P/E and P/B ratios across listed and OTC stocks (positive values only, so loss-makers and ETFs are excluded) over the past 1–10 years (default 5), with the distribution (min, quartiles, max), the latest day's percentile within that window, and month-end readings; optional market (`twse`/`tpex`) and end date |
| `get_chip_data` | API | Chip data for one stock: daily institutional flows, margin balances and main-force (top brokers) net, buy/sell streaks, major holders, insider pledges, and the latest day's top buying/selling branches (main-force data for holdings only) |
| `get_cagr_ranking` | API | Rank stocks by annualized return over M3-Y10 (price only, with dividends, or reinvested), filterable by market and industry |
| `get_stock_cagr` | API | Annualized and total return for one stock over every period |
| `get_portfolio_summary` | API | Personal holdings overview: market value, cost, unrealized gain, dividends received, industry weights, and market-value-weighted dividend yield and F-score, portfolio P/E and P/B (market-value-weighted harmonic means, loss-makers excluded); each holding also carries its official P/E and P/B, its latest monthly revenue YoY and consecutive YoY-decline months, the latest quarter's gross/operating/net margins and their year-over-year direction (not for financials), trailing four-quarter free cash flow, and fundamental warnings (revenue down YoY for 3+ months, all three margins down, operating loss, negative trailing free cash flow), with warned holdings listed in the summary (`member_id=0` for all members) |
| `get_portfolio_history` | API | Personal holdings performance over 1-120 months: time-weighted return (net of buys/sells, price only), annualized return, max drawdown, monthly returns, TAIEX comparison, and daily-return risk metrics against TAIEX (beta, annualized alpha, correlation, tracking error, volatility) |
| `get_holding_dividend_fill` | API | Dividend-fill status of the personal holdings (ETFs included) for every ex-dividend date in the last N days (default 365, 30–730): fill price (previous close), latest close, whether and on which trading day the price recovered, and the remaining gap for unfilled ones (largest gap first), with overall fill statistics |
| `screen_etfs` | API | Filter and sort Taiwan-listed ETFs (refreshed after each close) by category, region, distribution frequency, market and keyword; sort by trade value, assets, premium/discount, trailing-12-month yield, net creation, total return or holders. Each ETF includes close, estimated NAV, premium/discount, assets, net creation, distributions and next ex-date |
| `get_etf_detail` | API | One ETF in full: category, issuer, index, fees, close, estimated NAV and premium/discount (with 20-day average), assets, net creation, holders, distributions, market-price and NAV total returns (1 week to 5 years), 1-year risk, top-10 holdings, sector and asset allocation, and recent daily NAV/premium |
| `compare_etfs` | API | Compare 2–4 ETFs side by side: fees, assets, premium/discount (with 20-day average), yield and distribution frequency, 1-year market-price and NAV total return, 3-year annualized return, volatility, max drawdown, Sharpe ratio, and pairwise top-10 holdings overlap |
| `get_etf_holding_history` | API | An ETF's top-10 holdings and weights for each of the last 1–24 collected periods (periods follow actual collection dates, not fixed quarters) |
| `get_etf_proxy_tracking` | API | An ETF's one-year return difference against 0050 and the annualized volatility of that difference (at least 120 samples; a proxy, not tracking error against the actual index) |
| `analyze_stock_deep_dive` | API | One-call full stock analysis: quote and fundamentals, five-year valuation band, five-day chip data, revenue, margins, financial statements, financial health, technicals and dividends; missing data is flagged as warnings |
| `get_etf_holding_changes` | API | An ETF's top-10 holdings for the last few periods (default 6, roughly monthly), each with additions, removals and weight changes against the previous period |
| `get_portfolio_etf_exposure` | API | Look-through exposure of the personal holdings: held ETFs are split into their latest top-10 constituents by weight and merged with direct positions, listing the largest combined exposures (direct, via which ETFs, share of portfolio value) and how much of each ETF could be decomposed |
| `get_portfolio_rebalance` | API | Rebalance suggestion for the personal holdings against saved target weights (by stock, industry or ETF look-through): deviation, suggested lots and amounts, fees, tax and cash flow. For reference only; never places orders |
| `get_portfolio_targets` | API | Read the saved target weights and implied cash reserve per dimension |
| `set_portfolio_targets` | API | **Write tool**: saves target weights (sum ≤ 100%) for one dimension; used only as the rebalance baseline, never an order |
| `get_holding_chip` | API | Daily chip report for the personal holdings on a trading day (default latest): for each common stock still held at that close (ETFs excluded), net buy/sell by foreign investors, investment trusts and dealers with their share of volume, foreign/trust consecutive net-buy or net-sell days, margin and short balances with daily changes, and main-force (top brokers) net with the top buying and selling branches plus its sum over the last five main-force trading days; holdings that hit the daily Telegram alert thresholds are flagged with their reasons |
| `get_dividend_forecast` | API | Expected cash dividends from current holdings over the next 1-24 months: announced payouts plus last year's pattern, monthly totals, per-payout details, and forward yield |

Every tool is read-only except `set_portfolio_targets`, which only saves personal target weights. Response fields vary by tool: quote outputs include `data_as_of`, while other tools may use dates or ranges in their result objects instead. Nullable missing metrics remain `null`; empty collections and zero totals can be valid results. `get_dividend_forecast` explicitly separates announced and estimated dividends using each item's `basis` and the `announced_total` / `estimated_total` fields; estimates are not announced payouts.

## Requirements

- Go 1.27 (as specified in `go.mod`)
- Access to a compatible `stock_rust` Data API and its bearer key (the only supported data source)
- Production: an HTTPS reverse proxy such as Caddy or Nginx

## Quick start

```bash
git clone https://github.com/jiansoft/stock-mcp-go.git
cd stock-mcp-go
cp .env.example .env
```

At minimum, configure these values:

```dotenv
STOCK_RUST_API_BASE_URL=http://127.0.0.1:9002
STOCK_RUST_API_KEY=replace-with-a-dedicated-upstream-key
MCP_API_KEY=replace-with-an-initial-client-key
MCP_API_KEY_PEPPER=replace-with-at-least-32-random-bytes
MCP_ADMIN_TOKEN=replace-with-a-different-32-byte-random-secret
```

`MCP_API_KEY` is only a bootstrap value. It is imported when the API-key SQLite database is empty and can be removed from the environment after the first successful import.

Startup loads `.env` from the current working directory without overriding existing OS environment variables. The SQLite path is also relative to the working directory unless absolute; its directory must be writable by the service user.

Start the server:

```bash
go run .
```

The checked-in `.env.example` uses port `9005`; the application default is `3000` when `PORT` is unset.

Verify it:

```bash
curl http://127.0.0.1:9005/healthz
curl http://127.0.0.1:9005/readyz
```

Expected responses are `{"status":"ok"}`. `/readyz` returns HTTP 503 when the upstream Data API is unavailable or rejects the key.

`/healthz` checks only the HTTP process. `/readyz` performs an authenticated upstream stock search (`2330`, limit 1), with a three-second deadline and a two-second result cache; success does not verify all 56 tools or data freshness.

API documentation: open `http://127.0.0.1:9005/docs` (Swagger UI, no authentication). It is generated from the registered tools, so `tools/call` lists each tool's arguments; Try it out works with an MCP API key and adds the `Accept: application/json, text/event-stream` header that MCP requires.

## MCP client configuration

The endpoint accepts authenticated `POST` requests at `/mcp` by default:

```http
Authorization: Bearer <MCP_API_KEY>
```

Claude Code example:

```bash
claude mcp add --transport http stock-mcp https://mcp.example.com/mcp \
  --header "Authorization: Bearer <MCP_API_KEY>"
```

The server uses stateless Streamable HTTP. It does not provide stdio transport, server-side sessions, or an SSE subscription endpoint; `GET /mcp` and `DELETE /mcp` return `405 Method Not Allowed`.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `APP_ENV` | `development` | `development`, `production`, or `test` |
| `HOST` | `127.0.0.1` | Listen IP address (hostnames are rejected); use `0.0.0.0` inside a container |
| `PORT` | `3000` | HTTP port (`.env.example` overrides it to `9005`) |
| `MCP_PATH` | `/mcp` | MCP endpoint path |
| `TRUST_PROXY` | `false` | Trust proxy-appended client IP information |
| `TRUSTED_PROXY_HOPS` | `1` | Number of trusted proxies that append `X-Forwarded-For` |
| `STOCK_RUST_API_BASE_URL` | required | Upstream Data API base URL |
| `STOCK_RUST_API_KEY` | required | Dedicated upstream bearer key |
| `API_TIMEOUT_MS` | `30000` | Upstream HTTP timeout (multi-year statistics such as `get_institutional_streak_performance` can take several seconds on the Pi) |
| `MCP_API_KEY` | empty | One-time compatibility bootstrap key |
| `MCP_API_KEY_DB_PATH` | `data/mcp-api-keys.db` | SQLite API-key database path |
| `MCP_API_KEY_PEPPER` | required | HMAC pepper of at least 32 bytes |
| `MCP_ADMIN_TOKEN` | required | Separate administration secret of at least 32 bytes |
| `MCP_TRUSTED_ORIGINS` | empty | Comma-separated allowed browser origins |
| `RATE_LIMIT_WINDOW_MS` | `60000` | Rate-limit window |
| `RATE_LIMIT_MAX_REQUESTS` | `60` | Requests per API-key ID + source-IP combination per window; excess requests return HTTP 429 |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

The upstream key, MCP client keys, pepper, and admin token must all be separate secrets. Do not commit `.env`.

Tool-call log: every `tools/call` writes one `msg="MCP 工具呼叫"` entry with `tool`, `status` (`ok`, `tool_error`, `protocol_error`), `duration_ms`, and the caller's `key_prefix`; arguments are not logged. Use `LOG_LEVEL=info` in production.

## API-key administration

Open the embedded UI at:

```text
https://mcp.example.com/admin/mcp-api-keys
```

Sign in once with `MCP_ADMIN_TOKEN`. The server creates an eight-hour session held in memory and returns an `HttpOnly`, `SameSite=Strict` cookie. It sets `Secure` when the request uses TLS, or when `TRUST_PROXY=true` and `X-Forwarded-Proto=https`. Restarting the process clears all administration sessions. The token is not stored in browser storage.

The administration API also accepts `Authorization: Bearer <MCP_ADMIN_TOKEN>` for automation:

| Method | Endpoint | Action |
| --- | --- | --- |
| `POST` / `DELETE` | `/api/admin/session` | Sign in or sign out |
| `GET` / `POST` | `/api/admin/mcp-api-keys` | List or create keys |
| `GET` / `PATCH` / `DELETE` | `/api/admin/mcp-api-keys/{id}` | Read, update, or revoke a key |
| `POST` | `/api/admin/mcp-api-keys/{id}/enable` | Enable a key |
| `POST` | `/api/admin/mcp-api-keys/{id}/disable` | Disable a key |
| `POST` | `/api/admin/mcp-api-keys/{id}/rotate` | Rotate a key |

Complete API keys are returned only once, after creation or rotation. SQLite stores a public prefix and `HMAC-SHA-256(pepper, full-key)`, never the plaintext key. Changes are published to an immutable in-memory snapshot immediately after the database transaction commits.

Back up the SQLite key database and preserve its matching `MCP_API_KEY_PEPPER` separately. A different pepper causes startup to fail against the existing database; the database alone cannot restore usable keys. Stop the process before copying the database, or use a SQLite-aware backup that includes committed WAL data.

The API-key store supports one Go process with one local SQLite volume. Do not share the same SQLite file between replicas. A multi-replica deployment requires a shared relational key store and a cross-process refresh mechanism.

## Docker

Build and run from source:

```bash
docker compose -f docker-compose.example.yml up --build
```

The Compose example publishes `127.0.0.1:9004` to container port `3000` and persists API-key state in the `mcp-api-key-data` named volume. The image runs as a non-root user and uses the executable's `-health-check` mode because the distroless runtime contains no shell, `curl`, or `wget`.

Set `STOCK_RUST_API_BASE_URL` to an address reachable from the container; `127.0.0.1:9002` refers to the container itself. With Compose, point the host's reverse proxy to `127.0.0.1:9004`; the native-process examples below use `9005`.

`Dockerfile_live` and `control.sh` provide a separate ARM deployment flow for binaries produced by `build.ps1`. They support Linux ARM64 and ARMv7 and expect the selected `stock-mcp_linux_*` binary at the deployment root.

### Deploying to a Raspberry Pi (native process)

```powershell
.\scripts\deploy-armv7.ps1 -Build        # cross-compile, upload, control.sh update, verify
.\scripts\deploy-armv7.ps1 -StageOnly    # upload to /tmp and verify sha256 only
```

The script checks the ELF architecture and the uploaded sha256, replaces the remote `control.sh` when it differs from the repository copy (uploaded with LF line endings; `stop` now waits up to 90 seconds (override with `STOP_TIMEOUT`) for the old process to exit; if `update` still fails, the script waits up to 120 more seconds for the old process to exit and retries `update` once so the service is not left stopped, and `start` fails if the new process exits immediately), then verifies the process, port, `/healthz`, `/readyz`, and the startup log, printing rollback commands on failure (`control.sh` keeps the previous binary as `<binary>.<timestamp>`). Defaults: `pi@192.168.111.138:/opt/stock_mcp`, port `9005`. After the remote preflight it writes a deploy marker `/dev/shm/stock-deploying/stock-mcp` and appends `finished=` when the script ends (also on failure), so stock_crawler's host health check does not alert on swap or fsync spikes caused by the deploy within 30 minutes of the marker's last change.

## Reverse proxy

Production traffic must use HTTPS. A minimal Caddy configuration is:

```caddyfile
mcp.example.com {
    reverse_proxy 127.0.0.1:9005
}
```

Minimal Nginx configuration:

```nginx
location / {
    proxy_pass http://127.0.0.1:9005;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

Set `TRUST_PROXY=true` only when the service is actually behind a trusted proxy. `TRUSTED_PROXY_HOPS` must match the number of proxies that append `X-Forwarded-For`: use `1` for client → Nginx → server, or `2` for client → CDN → Nginx → server. A value that is too large can make the rate-limit IP identity spoofable.

Restrict the administration routes at the network edge when possible:

- `/admin/`
- `/api/admin/`

## Data and security semantics

- `get_market_movers` is the only tool whose `is_realtime` value can be `true`.
- Between the Taiwan market close and availability of the final daily data, movers may fall back to the previous trading day; the response reports its actual source and date.
- `get_realtime_snapshot` is a third-party near-real-time snapshot but is deliberately marked as not guaranteed real-time.
- Valuation bands, rankings, and screening results describe historical model output and are not targets or trading recommendations.
- QFII holdings are a latest snapshot, not a time series; the tool cannot infer accumulation or reduction trends.
- The MCP endpoint has a 1 MiB request-body limit; administration requests have a 64 KiB limit.
- Authentication failures are separately rate-limited before normal per-key request limiting.
- Secrets and authorization headers are excluded from logs.

## Development

```bash
make test        # go test ./...
make lint        # go vet ./...
make fmt-check   # verify gofmt formatting
make build       # build ./stock-mcp
```

## Known limitations

- Data freshness depends on the upstream `stock_rust` collection and processing schedule.
- Tool registration reflects the client implementation, not a capability probe of the deployed upstream. New tools require matching stock_rust endpoints and datasets; `/readyz` alone does not establish compatibility.
- Rate-limit counters and API-key verification snapshots are process-local.
- The SQLite API-key repository is designed for a single process and local persistent volume.
- The server exposes MCP tools only; it does not expose Resources, Prompts, or Sampling.
- All output is informational and does not constitute investment advice.
