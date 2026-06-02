package walletintel

import "time"

// FeatureSnapshot is a JSON-serializable bag of features evaluated during
// scoring. Every ScoreResult must include a snapshot — required for audit,
// reproduction, and admin diagnostics. Persisted as jsonb.
type FeatureSnapshot map[string]any

// ScoreResult is the canonical output of any scoring strategy.
type ScoreResult struct {
	Strategy        string
	Class           string
	Score           int
	Confidence      float64
	Promote         bool
	ReasonCodes     []string
	MissingData     []string
	FeatureSnapshot FeatureSnapshot
	ScoreVersion    string
}

// CLVSample is a single trade entry with optional post-trade price drift.
// Used by shark.clv_proxy_score.
type CLVSample struct {
	Direction     Direction
	EntryPrice    float64
	PriceAfter1h  float64 // 0 if unknown
	PriceAfter6h  float64
	PriceAfter24h float64
	HasAny        bool // true if any window has a known later price
}

// HolderRecord is a snapshot of this wallet's standing in a market at a
// point in time. Used by holder_quality_score / holder_concentration_score.
type HolderRecord struct {
	MarketSlug         string
	Rank               int     // 1 = top
	Amount             float64 // shares
	PctOutcomeSnapshot float64 // 0 if unknown
	PctValid           bool
	Persistence        int // number of repeat snapshots at top
}

// WalletFacts is the fully-assembled snapshot of a wallet at scoring time.
// All fields are observed values; never invented. Missing data must be
// signalled via *Known flags so scoring can record MISSING_* reason codes.
type WalletFacts struct {
	WalletID    string
	Wallet      string // proxy address
	Pseudonym   string
	ProfileSlug string

	// Activity
	TotalTrades          int
	TotalMarketsTraded   int
	ClosedPositionsCount int

	// PnL / payoff
	RealizedPnLKnown bool
	RealizedPnL      float64
	Wins             int
	Losses           int
	AvgWinSize       float64
	AvgLossSize      float64
	LargestWin       float64
	LargestLoss      float64

	// Trade sizing
	TradeSizeSamples []float64 // recent trade USDC sizes
	LargeBetCount    int
	LargeBetWins     int

	// Category focus
	CategoryDistribution map[string]int
	TargetCategories     []string

	// Recency
	LastTradeAt  time.Time
	Now          time.Time
	MaxStaleDays int

	// CLV (post-trade drift proxy)
	CLVSamples []CLVSample

	// Holder evidence
	Holders []HolderRecord

	// Data quality
	TakerOnlyHistory bool

	// Qualifying-trade evidence (normal whale path).
	QualifyingTradeCount     int
	QualifyingAvgNotional    float64
	QualifyingMedianNotional float64
	QualifyingMaxOddsBelow2  bool
	PartialTradeHistory      bool

	// Elite high-win whale evidence. Populated from closed positions
	// (`/positions` rows with size==0 AND known realized PnL sign).
	EliteClosedPositions    int
	EliteWinRate            float64
	EliteAvgEntryNotional   float64
	EliteTotalEntryNotional float64
	EliteROI                float64
	EliteAvgOdds            float64
	ElitePayoffRatio        float64
	EliteRecentDrawdown     bool
	ElitePnLKnown           bool

	// Realized trading profitability (v5 primary signal).
	// Sourced from wallet_realized_trades populated by
	// ReconstructRealizedTrades from the wallet's BUY/SELL history.
	//
	// IMPORTANT: A "realized profitable cycle" means the wallet exited a
	// position at a higher price than the weighted-average entry. It is
	// trading profit, never inferred from the market's final YES/NO
	// resolution. A wallet that bought YES @ 0.20 and sold YES @ 0.45
	// records a profitable cycle, even if the market later resolves NO.
	RealizedCyclesCount           int
	RealizedProfitableCyclesCount int
	RealizedLosingCyclesCount     int
	RealizedWinRate               float64 // profitable_exit_rate
	RealizedTotalPnL              float64
	RealizedTotalVolume           float64 // sum of all entry notionals across realized cycles
	RealizedAvgNotional           float64
	RealizedMedianNotional        float64
	RealizedAvgROI                float64
	RealizedProfitFactor          float64
	RealizedMaxWin                float64
	RealizedMaxLoss               float64
	LastRealizedExitAt            time.Time
	// ScoringBasis is "realized_trading_pnl" when wallet_realized_trades has
	// sufficient sample, "api_closed_positions" when only the API closed
	// stats are usable, "proxy_partial" when both are partial. Carried into
	// the score feature_snapshot so operators see which signal was used.
	ScoringBasis string

	// Historical closed-position evidence (v4 shark gates, secondary signal).
	// Sourced exclusively from wallet_closed_position_latest populated by
	// HistoryBackfillWorker draining the wallet's /closed-positions stream.
	// Open positions, current holder size, and in-service lifecycle MUST
	// NOT be used here — these fields are the only ROI/win-rate input.
	ClosedPositionsCountHist    int
	ProfitableClosedPositions   int
	LosingClosedPositions       int
	HistoricalTotalBoughtClosed float64
	HistoricalRealizedPnL       float64
	HistoricalAvgClosedStake    float64
	HistoricalMedianClosedStake float64
	HistoricalWinRate           float64
	HistoricalROI               float64
	HistoricalMaxWin            float64
	HistoricalMaxLoss           float64
	HistoricalPnLKnown          bool
	ClosedPositionsComplete     bool
	TradesBackfillComplete      bool
	LastClosedAt                time.Time
	// DataQuality labels how reliable the historical closed-position sample
	// is. complete | partial_offset_cap | partial_safety_cap | partial_local_cap | proxy | missing.
	DataQuality                  string
	TradesPartialReason          string
	ClosedPositionsPartialReason string

	// Profile all-time P&L (sourced from /positions cashPnl sum — same as
	// Polymarket UI "all-time P&L"). Populated from GetUserSummary in runner.
	// When known and strongly negative, this is a hard veto on active promotion.
	ProfileCashPnL            float64
	ProfileCashPnLKnown       bool
	ProfileCashPnLSampleCount int // positions the sum is based on

	// Position universe breadth (from wallet_closed_position_latest). Used to detect
	// the case where a tiny closed-position sample (2–5%) is being scored while
	// 95%+ of positions remain open and potentially underwater.
	HistoricalTotalPositionCount int // total rows in wallet_closed_position_latest
	HistoricalOpenPositionCount  int // is_closed=false rows

	// Insider-specific context (when scoring a NEW BET)
	NewBet *NewBetContext

	// Lifetime view consumed by insider-like scoring. Distinct from
	// TotalTrades so callers can present trade count without forcing a
	// re-pagination of /trades for every score.
	LifetimeTradeCount      int
	LifetimeProfitableCount int
	LifetimeLosingCount     int
	FirstTradeAt            time.Time
	InsiderStreakClean      bool // true iff LifetimeLosingCount == 0

	// Rolling window performance (used by high-frequency + profitability gates).
	WeeklyTradeCount        int
	WeeklyCoverage          time.Duration
	WeeklyAvgTradeInterval  time.Duration
	WeeklyRealizedCycles    int
	WeeklyRealizedPnL       float64
	WeeklyEntryNotional     float64
	WeeklyProfitPct         float64
	WeeklyProfitPctKnown    bool
	MonthlyTradeCount       int
	MonthlyCoverage         time.Duration
	MonthlyAvgTradeInterval time.Duration
	MonthlyRealizedCycles   int
	MonthlyRealizedPnL      float64
	MonthlyEntryNotional    float64
	MonthlyProfitPct        float64
	MonthlyProfitPctKnown   bool
}

// NewBetContext describes the specific trade that triggered insider scoring.
// Optional — present only when scoring around a new bet event.
type NewBetContext struct {
	Direction          Direction
	Notional           float64 // USDC
	Price              float64
	Outcome            string
	MarketSlug         string
	MarketCategory     string
	MarketIsHighImpact bool
	NearCatalyst       bool
	CatalystKnown      bool

	StreakCount         int // consecutive prior wins
	TopHolderInMarket   bool
	HolderRankInMarket  int // 0 if unknown
	PctOutcomeInMarket  float64
	PctValid            bool
	SuddenConcentration bool
}

// SharkParams mirrors the relevant config knobs so scoring stays pure.
type SharkParams struct {
	MinTrades          int
	MinClosedPositions int
	MinScore           int
	MinConfidence      float64
	MaxStaleDays       int

	// Normal-path qualifying gate. Deprecated for v4 promotion (kept for
	// diagnostics & v3 reconciliation; not used by ScoreShark in v4).
	QualifyingMinTrades      int
	QualifyingMinNotionalUSD float64
	QualifyingMinOdds        float64
	QualifyingMinAvgNotional float64

	// Elite high-win whale gates. Deprecated for v4 promotion (kept for
	// diagnostics; not used by ScoreShark in v4).
	EliteMinClosedPositions    int
	EliteMinWinRate            float64
	EliteMinAvgEntryNotional   float64
	EliteMinTotalEntryNotional float64
	EliteMinROI                float64
	EliteMinAvgOdds            float64
	EliteMinPayoffRatio        float64

	// v4 historical-shark gates (kept for diagnostics; NOT used as hard gates in v5).
	HistMinClosedPositions int     // default 25
	HistMinROI             float64 // DIAGNOSTIC ONLY in v5 — not a hard gate
	HistMinWinRate         float64 // DIAGNOSTIC ONLY in v5 — not a hard gate
	HistMinAvgStakeUSD     float64 // replaced by VolumeMinAvgTrade in v5

	// v5 high_volume_profitable_shark gates. All must pass to promote.
	VolumeMinTotalPnL     float64 // realized total PnL; default 50_000
	VolumeMinAvgTrade     float64 // avg realized trade notional; default 5_000
	VolumeMinTotalVolume  float64 // total realized volume; default 500_000
	VolumeMinExitRate     float64 // profitable exit rate; default 0.60
	VolumeMinProfitFactor float64 // profit factor; default 1.25; API-only path uses exit_rate proxy instead
	VolumeMinCycles       int     // min realized cycles for sample validity; default 10

	// High-frequency + profitability gates.
	MaxAvgTradeInterval time.Duration // default 2m
	MinWindowProfitPct  float64       // default 0.30
}

// InsiderParams mirrors the relevant config knobs.
type InsiderParams struct {
	MaxLifetimeTrades  int
	MaxLifetimeMarkets int
	MinNotionalUSD     float64
	MinScore           int
	MinConfidence      float64
	LowProbPriceThr    float64
	// v4 insider gates (independent from shark ROI/win-rate).
	MinOdds               float64 // default 3.0 (price <= 1/3)
	MaxLifetimeForCapture int     // default 10 — wallets above this are not initial insider candidates
	HighImpactCategories  []string

	// High-frequency + profitability gates.
	MaxAvgTradeInterval time.Duration // default 2m
	MinWindowProfitPct  float64       // default 0.30
}
