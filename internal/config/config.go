// Package config loads and validates watchtower runtime configuration.
//
// All required keys (DB, Telegram, Polymarket base URLs) must be present;
// numeric thresholds have safe non-secret defaults. Fail-fast on missing
// secrets so the binary never starts with an empty bot token / chat ID.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Database
	DatabaseURL string

	// Telegram
	TelegramBotToken       string
	TelegramAdminChatID    string
	TelegramBetsChatID     string
	TelegramClustersChatID string
	TelegramNewsChatID     string

	// Polymarket
	PolymarketGammaBaseURL   string
	PolymarketDataAPIBaseURL string
	PolymarketCLOBBaseURL    string
	PolymarketWSURL          string

	// Intervals
	DiscoveryInterval         time.Duration
	HolderScanInterval        time.Duration
	WatchedWalletPollInterval time.Duration
	ClusterScanInterval       time.Duration
	NewsScanInterval          time.Duration

	// Hotset
	HotsetMaxMarkets int

	// Candidate discovery (v4 expansion).
	HolderDeepScanSize         int     // markets deep-scanned for top-20 evidence (default 50)
	CandidateTopMarketsLimit   int     // overall market scan cap (default 80)
	LeaderboardTopWalletsLimit int     // leaderboard probe size (default 200)
	LargeTradeMinNotionalUSD   float64 // threshold for "large trade" candidate sourcing (default 20_000)

	// Historical backfill worker.
	HistoryBackfillInterval       time.Duration
	HistoryBackfillBatchSize      int
	HistoryBackfillConcurrency    int
	HistoryBackfillTradePageSize  int
	HistoryBackfillClosedPageSize int
	HistoryBackfillMaxTradePages  int
	HistoryBackfillMaxClosedPages int

	// v4 historical-shark gates.
	SharkHistMinClosedPositions int
	SharkHistMinROI             float64
	SharkHistMinWinRate         float64
	SharkHistMinAvgStakeUSD     float64

	// v4 insider-like additional gates.
	InsiderMinOdds               float64
	InsiderMaxLifetimeForCapture int

	// Shark strategy
	SharkMinTrades          int
	SharkMinClosedPositions int
	SharkMinScore           int
	SharkMinConfidence      float64
	SharkMaxStaleDays       int

	// Shark qualification (v3.0.0 whale-only).
	SharkQualifyingMinTrades      int     // default 100
	SharkQualifyingMinNotionalUSD float64 // default 20000
	SharkQualifyingMinOdds        float64 // default 2.0
	SharkQualifyingMinAvgNotional float64 // default 20000

	// Elite high-win whale path (second promotion route).
	EliteMinClosedPositions    int     // default 25
	EliteMinWinRate            float64 // default 0.80
	EliteMinAvgEntryNotional   float64 // default 100_000
	EliteMinTotalEntryNotional float64 // default 2_500_000
	EliteMinROI                float64 // default 0.3333
	EliteMinAvgOdds            float64 // default 2.0
	EliteMinPayoffRatio        float64 // default 1.3333

	// Per-bet minimum notional for user-facing SHARK_BET alerts. Bets below
	// are still persisted but never emit individual alerts. Default 20_000.
	// Kept as legacy dust floor; replaced by profit-tier gate when enabled.
	SharkAlertMinNotionalUSD float64

	// Profit-tier gate for SHARK_BET alerts (replaces simple min_notional).
	// When enabled, each trade is classified into a tier by notional and must
	// satisfy both a minimum odds and minimum profit-if-win for that tier.
	AlertProfitGateEnabled bool

	AlertTinyMaxNotionalUSD float64 // default 500
	AlertTinyMinOdds        float64 // default 10
	AlertTinyMinProfitUSD   float64 // default 4000

	AlertSmallMaxNotionalUSD float64 // default 2000
	AlertSmallMinOdds        float64 // default 7
	AlertSmallMinProfitUSD   float64 // default 7000

	AlertMediumMaxNotionalUSD float64 // default 10000
	AlertMediumMinOdds        float64 // default 4
	AlertMediumMinProfitUSD   float64 // default 15000

	AlertLargeMaxNotionalUSD float64 // default 80000
	AlertLargeMinOdds        float64 // default 2
	AlertLargeMinProfitUSD   float64 // default 25000

	AlertMegaMinNotionalUSD float64 // default 80000
	AlertMegaMinOdds        float64 // default 1.15
	AlertMegaMinProfitUSD   float64 // default 10000

	// Cluster profit gate (additive to existing notional gate).
	ClusterMinTotalProfitUSD float64 // default 25000
	ClusterMinAvgOdds        float64 // default 2.0

	// Single-trader burst (same-wallet aggregation).
	BurstWindow              time.Duration // default 15m
	BurstMinBets             int           // default 3
	BurstMinDistinctMarkets  int           // default 2
	BurstMinTotalNotionalUSD float64       // default 60_000

	// Insider strategy
	InsiderMaxLifetimeTrades  int
	InsiderMaxLifetimeMarkets int
	InsiderMinNotionalUSD     float64
	InsiderMinScore           int
	InsiderMinConfidence      float64
	InsiderLowProbPriceThr    float64

	// Cluster
	ClusterWindowBefore     time.Duration
	ClusterWindowAfter      time.Duration
	ClusterMinWallets       int
	ClusterMinTotalNotional float64
	ClusterMinQualityScore  int

	// News
	NextJSNewsEnabled bool
	NextJSBuildIDTTL  time.Duration

	// Rate limits (req/sec)
	GammaRPSLimit    float64
	DataAPIRPSLimit  float64
	CLOBRPSLimit     float64
	TelegramRPSLimit float64

	// Runtime
	HTTPTimeout       time.Duration
	WorkerConcurrency int
	LogLevel          string
	MetricsAddr       string

	// Optional dashboard base
	InternalDashboardBaseURL string

	// Price sampler
	PriceSamplerInterval    time.Duration
	PriceSamplerMaxPerCycle int

	// Telegram retry
	TelegramRetryInterval time.Duration
	TelegramMaxAttempts   int

	// Lifecycle / exit alerts
	LifecycleEnabled       bool
	ExitAlertsEnabled      bool
	ExitFullCloseTolerance float64 // fraction of remaining size considered "full close" (default 0.05)
	ExitClusterEnabled     bool    // include exits in cluster scan (default false)

	// Safety switch: when false, AlertRouter persists decisions/deliveries
	// with status="skipped" and does NOT call Telegram.
	AlertingEnabled bool

	// Categories of interest (tag slugs)
	TargetCategories []string
}

func Load() (*Config, error) { return LoadFromEnv(os.Getenv) }

// LoadFromEnv is the testable entrypoint — pass a getter to inject env values.
func LoadFromEnv(get func(string) string) (*Config, error) {
	c := &Config{
		DatabaseURL:              get("DATABASE_URL"),
		TelegramBotToken:         get("TELEGRAM_BOT_TOKEN"),
		TelegramAdminChatID:      get("TELEGRAM_ADMIN_CHAT_ID"),
		TelegramBetsChatID:       get("TELEGRAM_BETS_CHAT_ID"),
		TelegramClustersChatID:   get("TELEGRAM_CLUSTERS_CHAT_ID"),
		TelegramNewsChatID:       get("TELEGRAM_NEWS_CHAT_ID"),
		PolymarketGammaBaseURL:   strOr(get("POLYMARKET_GAMMA_BASE_URL"), "https://gamma-api.polymarket.com"),
		PolymarketDataAPIBaseURL: strOr(get("POLYMARKET_DATA_API_BASE_URL"), "https://data-api.polymarket.com"),
		PolymarketCLOBBaseURL:    strOr(get("POLYMARKET_CLOB_BASE_URL"), "https://clob.polymarket.com"),
		PolymarketWSURL:          strOr(get("POLYMARKET_WS_URL"), "wss://ws-subscriptions-clob.polymarket.com/ws/market"),
		LogLevel:                 strOr(get("LOG_LEVEL"), "info"),
		MetricsAddr:              strOr(get("METRICS_ADDR"), ":9090"),
		InternalDashboardBaseURL: get("INTERNAL_DASHBOARD_BASE_URL"),
	}

	var err error
	if c.DiscoveryInterval, err = parseDur(get("DISCOVERY_INTERVAL"), 5*time.Minute); err != nil {
		return nil, err
	}
	if c.HolderScanInterval, err = parseDur(get("HOLDER_SCAN_INTERVAL"), 10*time.Minute); err != nil {
		return nil, err
	}
	if c.WatchedWalletPollInterval, err = parseDur(get("WATCHED_WALLET_POLL_INTERVAL"), 1*time.Minute); err != nil {
		return nil, err
	}
	if c.ClusterScanInterval, err = parseDur(get("CLUSTER_SCAN_INTERVAL"), 90*time.Second); err != nil {
		return nil, err
	}
	if c.NewsScanInterval, err = parseDur(get("NEWS_SCAN_INTERVAL"), 5*time.Minute); err != nil {
		return nil, err
	}
	if c.HTTPTimeout, err = parseDur(get("HTTP_TIMEOUT"), 20*time.Second); err != nil {
		return nil, err
	}

	// Support HOTSET_MARKETS_LIMIT as the canonical name (recommended ≥400);
	// fall back to HOTSET_MAX_MARKETS for backward compatibility.
	hotsetLimitRaw := get("HOTSET_MARKETS_LIMIT")
	if hotsetLimitRaw == "" {
		hotsetLimitRaw = get("HOTSET_MAX_MARKETS")
	}
	if c.HotsetMaxMarkets, err = parseInt(hotsetLimitRaw, 400); err != nil {
		return nil, err
	}
	if c.HolderDeepScanSize, err = parseInt(get("HOLDER_DEEP_SCAN_SIZE"), 100); err != nil {
		return nil, err
	}
	if c.CandidateTopMarketsLimit, err = parseInt(get("CANDIDATE_TOP_MARKETS_LIMIT"), 200); err != nil {
		return nil, err
	}
	if c.LeaderboardTopWalletsLimit, err = parseInt(get("LEADERBOARD_TOP_WALLETS_LIMIT"), 200); err != nil {
		return nil, err
	}
	if c.LargeTradeMinNotionalUSD, err = parseFloat(get("LARGE_TRADE_MIN_NOTIONAL_USD"), 20_000); err != nil {
		return nil, err
	}

	if c.HistoryBackfillInterval, err = parseDur(get("HISTORY_BACKFILL_INTERVAL"), 2*time.Minute); err != nil {
		return nil, err
	}
	if c.HistoryBackfillBatchSize, err = parseInt(get("HISTORY_BACKFILL_BATCH_SIZE"), 30); err != nil {
		return nil, err
	}
	if c.HistoryBackfillConcurrency, err = parseInt(get("HISTORY_BACKFILL_CONCURRENCY"), 4); err != nil {
		return nil, err
	}
	if c.HistoryBackfillTradePageSize, err = parseInt(get("HISTORY_BACKFILL_TRADE_PAGE_SIZE"), 500); err != nil {
		return nil, err
	}
	if c.HistoryBackfillClosedPageSize, err = parseInt(get("HISTORY_BACKFILL_CLOSED_PAGE_SIZE"), 500); err != nil {
		return nil, err
	}
	if c.HistoryBackfillMaxTradePages, err = parseInt(get("HISTORY_BACKFILL_MAX_TRADE_PAGES"), 20); err != nil {
		return nil, err
	}
	if c.HistoryBackfillMaxClosedPages, err = parseInt(get("HISTORY_BACKFILL_MAX_CLOSED_PAGES"), 20); err != nil {
		return nil, err
	}

	// v4 historical-shark gates.
	if c.SharkHistMinClosedPositions, err = parseInt(get("SHARK_HIST_MIN_CLOSED_POSITIONS"), 25); err != nil {
		return nil, err
	}
	if c.SharkHistMinROI, err = parseFloat(get("SHARK_HIST_MIN_ROI"), 0.10); err != nil {
		return nil, err
	}
	if c.SharkHistMinWinRate, err = parseFloat(get("SHARK_HIST_MIN_WIN_RATE"), 0.75); err != nil {
		return nil, err
	}
	if c.SharkHistMinAvgStakeUSD, err = parseFloat(get("SHARK_HIST_MIN_AVG_STAKE_USD"), 10_000); err != nil {
		return nil, err
	}

	// v4 insider-like additional gates.
	if c.InsiderMinOdds, err = parseFloat(get("INSIDER_MIN_ODDS"), 3.0); err != nil {
		return nil, err
	}
	if c.InsiderMaxLifetimeForCapture, err = parseInt(get("INSIDER_MAX_LIFETIME_FOR_CAPTURE"), 10); err != nil {
		return nil, err
	}

	// Shark
	if c.SharkMinTrades, err = parseInt(get("SHARK_MIN_TRADES"), 100); err != nil {
		return nil, err
	}
	if c.SharkMinClosedPositions, err = parseInt(get("SHARK_MIN_CLOSED_POSITIONS"), 30); err != nil {
		return nil, err
	}
	if c.SharkMinScore, err = parseInt(get("SHARK_MIN_SCORE"), 70); err != nil {
		return nil, err
	}
	if c.SharkMinConfidence, err = parseFloat(get("SHARK_MIN_CONFIDENCE"), 0.65); err != nil {
		return nil, err
	}
	if c.SharkMaxStaleDays, err = parseInt(get("SHARK_MAX_STALE_DAYS"), 21); err != nil {
		return nil, err
	}
	if c.SharkQualifyingMinTrades, err = parseInt(get("SHARK_QUALIFYING_MIN_TRADES"), 100); err != nil {
		return nil, err
	}
	if c.SharkQualifyingMinNotionalUSD, err = parseFloat(get("SHARK_QUALIFYING_MIN_NOTIONAL_USD"), 20_000); err != nil {
		return nil, err
	}
	if c.SharkQualifyingMinOdds, err = parseFloat(get("SHARK_QUALIFYING_MIN_ODDS"), 2.0); err != nil {
		return nil, err
	}
	if c.SharkQualifyingMinAvgNotional, err = parseFloat(get("SHARK_QUALIFYING_MIN_AVG_NOTIONAL"), 20_000); err != nil {
		return nil, err
	}
	if c.EliteMinClosedPositions, err = parseInt(get("ELITE_MIN_CLOSED_POSITIONS"), 25); err != nil {
		return nil, err
	}
	if c.EliteMinWinRate, err = parseFloat(get("ELITE_MIN_WIN_RATE"), 0.80); err != nil {
		return nil, err
	}
	if c.EliteMinAvgEntryNotional, err = parseFloat(get("ELITE_MIN_AVG_ENTRY_NOTIONAL"), 100_000); err != nil {
		return nil, err
	}
	if c.EliteMinTotalEntryNotional, err = parseFloat(get("ELITE_MIN_TOTAL_ENTRY_NOTIONAL"), 2_500_000); err != nil {
		return nil, err
	}
	if c.EliteMinROI, err = parseFloat(get("ELITE_MIN_ROI"), 0.3333); err != nil {
		return nil, err
	}
	if c.EliteMinAvgOdds, err = parseFloat(get("ELITE_MIN_AVG_ODDS"), 2.0); err != nil {
		return nil, err
	}
	if c.EliteMinPayoffRatio, err = parseFloat(get("ELITE_MIN_PAYOFF_RATIO"), 1.3333); err != nil {
		return nil, err
	}
	if c.SharkAlertMinNotionalUSD, err = parseFloat(get("SHARK_ALERT_MIN_NOTIONAL_USD"), 10_000); err != nil {
		return nil, err
	}

	// Profit-tier gate
	c.AlertProfitGateEnabled = parseBool(get("ALERT_PROFIT_GATE_ENABLED"), true)
	if c.AlertTinyMaxNotionalUSD, err = parseFloat(get("ALERT_TINY_MAX_NOTIONAL_USD"), 500); err != nil {
		return nil, err
	}
	if c.AlertTinyMinOdds, err = parseFloat(get("ALERT_TINY_MIN_ODDS"), 10); err != nil {
		return nil, err
	}
	if c.AlertTinyMinProfitUSD, err = parseFloat(get("ALERT_TINY_MIN_PROFIT_USD"), 4_000); err != nil {
		return nil, err
	}
	if c.AlertSmallMaxNotionalUSD, err = parseFloat(get("ALERT_SMALL_MAX_NOTIONAL_USD"), 2_000); err != nil {
		return nil, err
	}
	if c.AlertSmallMinOdds, err = parseFloat(get("ALERT_SMALL_MIN_ODDS"), 7); err != nil {
		return nil, err
	}
	if c.AlertSmallMinProfitUSD, err = parseFloat(get("ALERT_SMALL_MIN_PROFIT_USD"), 7_000); err != nil {
		return nil, err
	}
	if c.AlertMediumMaxNotionalUSD, err = parseFloat(get("ALERT_MEDIUM_MAX_NOTIONAL_USD"), 10_000); err != nil {
		return nil, err
	}
	if c.AlertMediumMinOdds, err = parseFloat(get("ALERT_MEDIUM_MIN_ODDS"), 4); err != nil {
		return nil, err
	}
	if c.AlertMediumMinProfitUSD, err = parseFloat(get("ALERT_MEDIUM_MIN_PROFIT_USD"), 15_000); err != nil {
		return nil, err
	}
	if c.AlertLargeMaxNotionalUSD, err = parseFloat(get("ALERT_LARGE_MAX_NOTIONAL_USD"), 80_000); err != nil {
		return nil, err
	}
	if c.AlertLargeMinOdds, err = parseFloat(get("ALERT_LARGE_MIN_ODDS"), 2); err != nil {
		return nil, err
	}
	if c.AlertLargeMinProfitUSD, err = parseFloat(get("ALERT_LARGE_MIN_PROFIT_USD"), 25_000); err != nil {
		return nil, err
	}
	if c.AlertMegaMinNotionalUSD, err = parseFloat(get("ALERT_MEGA_MIN_NOTIONAL_USD"), 80_000); err != nil {
		return nil, err
	}
	if c.AlertMegaMinOdds, err = parseFloat(get("ALERT_MEGA_MIN_ODDS"), 1.15); err != nil {
		return nil, err
	}
	if c.AlertMegaMinProfitUSD, err = parseFloat(get("ALERT_MEGA_MIN_PROFIT_USD"), 10_000); err != nil {
		return nil, err
	}
	if c.ClusterMinTotalProfitUSD, err = parseFloat(get("CLUSTER_MIN_TOTAL_PROFIT_USD"), 25_000); err != nil {
		return nil, err
	}
	if c.ClusterMinAvgOdds, err = parseFloat(get("CLUSTER_MIN_AVG_ODDS"), 2.0); err != nil {
		return nil, err
	}

	if c.BurstWindow, err = parseDur(get("BURST_WINDOW"), 15*time.Minute); err != nil {
		return nil, err
	}
	if c.BurstMinBets, err = parseInt(get("BURST_MIN_BETS"), 3); err != nil {
		return nil, err
	}
	if c.BurstMinDistinctMarkets, err = parseInt(get("BURST_MIN_DISTINCT_MARKETS"), 2); err != nil {
		return nil, err
	}
	if c.BurstMinTotalNotionalUSD, err = parseFloat(get("BURST_MIN_TOTAL_NOTIONAL_USD"), 60_000); err != nil {
		return nil, err
	}

	// Insider
	if c.InsiderMaxLifetimeTrades, err = parseInt(get("INSIDER_MAX_LIFETIME_TRADES"), 3); err != nil {
		return nil, err
	}
	if c.InsiderMaxLifetimeMarkets, err = parseInt(get("INSIDER_MAX_LIFETIME_MARKETS"), 3); err != nil {
		return nil, err
	}
	if c.InsiderMinNotionalUSD, err = parseFloat(get("INSIDER_MIN_NOTIONAL_USD"), 20000); err != nil {
		return nil, err
	}
	if c.InsiderMinScore, err = parseInt(get("INSIDER_MIN_SCORE"), 70); err != nil {
		return nil, err
	}
	if c.InsiderMinConfidence, err = parseFloat(get("INSIDER_MIN_CONFIDENCE"), 0.60); err != nil {
		return nil, err
	}
	if c.InsiderLowProbPriceThr, err = parseFloat(get("INSIDER_LOW_PROB_PRICE_THRESHOLD"), 0.20); err != nil {
		return nil, err
	}

	// Cluster
	if c.ClusterWindowBefore, err = parseDur(get("CLUSTER_WINDOW_BEFORE"), 3*time.Hour); err != nil {
		return nil, err
	}
	if c.ClusterWindowAfter, err = parseDur(get("CLUSTER_WINDOW_AFTER"), 3*time.Hour); err != nil {
		return nil, err
	}
	if c.ClusterMinWallets, err = parseInt(get("CLUSTER_MIN_WALLETS"), 2); err != nil {
		return nil, err
	}
	if c.ClusterMinTotalNotional, err = parseFloat(get("CLUSTER_MIN_TOTAL_NOTIONAL_USD"), 5000); err != nil {
		return nil, err
	}
	if c.ClusterMinQualityScore, err = parseInt(get("CLUSTER_MIN_QUALITY_SCORE"), 60); err != nil {
		return nil, err
	}

	// News
	c.NextJSNewsEnabled = parseBool(get("NEXTJS_NEWS_ENABLED"), false)
	if c.NextJSBuildIDTTL, err = parseDur(get("NEXTJS_BUILD_ID_TTL"), 30*time.Minute); err != nil {
		return nil, err
	}

	// Rate limits
	if c.GammaRPSLimit, err = parseFloat(get("GAMMA_RPS_LIMIT"), 8); err != nil {
		return nil, err
	}
	if c.DataAPIRPSLimit, err = parseFloat(get("DATA_API_RPS_LIMIT"), 24); err != nil {
		return nil, err
	}
	if c.CLOBRPSLimit, err = parseFloat(get("CLOB_RPS_LIMIT"), 8); err != nil {
		return nil, err
	}
	if c.TelegramRPSLimit, err = parseFloat(get("TELEGRAM_RPS_LIMIT"), 1); err != nil {
		return nil, err
	}
	if c.WorkerConcurrency, err = parseInt(get("WORKER_CONCURRENCY"), 6); err != nil {
		return nil, err
	}
	if c.PriceSamplerInterval, err = parseDur(get("PRICE_SAMPLER_INTERVAL"), 2*time.Minute); err != nil {
		return nil, err
	}
	if c.PriceSamplerMaxPerCycle, err = parseInt(get("PRICE_SAMPLER_MAX_PER_CYCLE"), 40); err != nil {
		return nil, err
	}
	if c.TelegramRetryInterval, err = parseDur(get("TELEGRAM_RETRY_INTERVAL"), 30*time.Second); err != nil {
		return nil, err
	}
	if c.TelegramMaxAttempts, err = parseInt(get("TELEGRAM_MAX_ATTEMPTS"), 5); err != nil {
		return nil, err
	}
	c.LifecycleEnabled = parseBool(get("LIFECYCLE_ENABLED"), true)
	c.ExitAlertsEnabled = parseBool(get("EXIT_ALERTS_ENABLED"), true)
	if c.ExitFullCloseTolerance, err = parseFloat(get("EXIT_FULL_CLOSE_TOLERANCE"), 0.05); err != nil {
		return nil, err
	}
	c.ExitClusterEnabled = parseBool(get("EXIT_CLUSTER_ENABLED"), false)
	c.AlertingEnabled = parseBool(get("ALERTING_ENABLED"), true)

	cats := get("TARGET_CATEGORIES")
	if cats == "" {
		cats = "politics,geopolitics,war,military,elections"
	}
	for _, s := range strings.Split(cats, ",") {
		s = strings.TrimSpace(strings.ToLower(s))
		if s != "" {
			c.TargetCategories = append(c.TargetCategories, s)
		}
	}

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Validate() error {
	miss := func(k string) error { return fmt.Errorf("config: required env %q missing", k) }
	if c.DatabaseURL == "" {
		return miss("DATABASE_URL")
	}
	if c.TelegramBotToken == "" {
		return miss("TELEGRAM_BOT_TOKEN")
	}
	if c.TelegramAdminChatID == "" {
		return miss("TELEGRAM_ADMIN_CHAT_ID")
	}
	if c.TelegramBetsChatID == "" {
		return miss("TELEGRAM_BETS_CHAT_ID")
	}
	if c.TelegramClustersChatID == "" {
		return miss("TELEGRAM_CLUSTERS_CHAT_ID")
	}
	if c.TelegramNewsChatID == "" {
		return miss("TELEGRAM_NEWS_CHAT_ID")
	}
	if c.SharkMinScore < 0 || c.SharkMinScore > 100 {
		return fmt.Errorf("config: SHARK_MIN_SCORE out of range")
	}
	if c.InsiderMinScore < 0 || c.InsiderMinScore > 100 {
		return fmt.Errorf("config: INSIDER_MIN_SCORE out of range")
	}
	if c.SharkMinConfidence < 0 || c.SharkMinConfidence > 1 {
		return fmt.Errorf("config: SHARK_MIN_CONFIDENCE must be in [0,1]")
	}
	if c.InsiderMinConfidence < 0 || c.InsiderMinConfidence > 1 {
		return fmt.Errorf("config: INSIDER_MIN_CONFIDENCE must be in [0,1]")
	}
	if c.ClusterMinWallets < 2 {
		return fmt.Errorf("config: CLUSTER_MIN_WALLETS must be >= 2")
	}
	if c.GammaRPSLimit <= 0 || c.DataAPIRPSLimit <= 0 || c.CLOBRPSLimit <= 0 || c.TelegramRPSLimit <= 0 {
		return fmt.Errorf("config: RPS limits must be > 0")
	}
	if c.HotsetMaxMarkets <= 0 {
		return fmt.Errorf("config: HOTSET_MAX_MARKETS must be > 0")
	}
	return nil
}

func strOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func parseDur(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("config: invalid duration %q: %w", s, err)
	}
	return d, nil
}

func parseInt(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("config: invalid int %q: %w", s, err)
	}
	return v, nil
}

func parseFloat(s string, def float64) (float64, error) {
	if s == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("config: invalid float %q: %w", s, err)
	}
	return v, nil
}

func parseBool(s string, def bool) bool {
	if s == "" {
		return def
	}
	switch strings.ToLower(s) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}
