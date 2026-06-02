// Package alerts contains the centralized alert decision/router and message
// formatters. NO other package may call the Telegram client directly.
package alerts

import "time"

// Channel names. Map to Telegram chat IDs in config.
const (
	ChannelAdmin    = "admin"
	ChannelBets     = "bets"
	ChannelClusters = "clusters"
	ChannelNews     = "news"
)

// AlertType drives formatting/routing.
const (
	TypeSharkBet               = "SHARK_BET"
	TypeInsiderBet             = "INSIDER_BET"
	TypeInsiderFirstBigBet     = "INSIDER_LIKE_FIRST_BIG_BET"
	TypeInsiderStreakContinues = "INSIDER_LIKE_STREAK_CONTINUES"
	TypeInsiderStreakBroken    = "INSIDER_LIKE_STREAK_BROKEN"
	TypeClusterBet             = "CLUSTER_BET"
	TypeNews                   = "NEWS"
	TypePositionExit           = "POSITION_EXIT"
	TypeSharkBurst             = "SHARK_BURST"
	// Admin-only discovery / lifecycle alerts.
	TypeSharkDiscovered             = "SHARK_DISCOVERED"
	TypeInsiderLikeDiscovered       = "INSIDER_LIKE_DISCOVERED"
	TypeWalletDemoted               = "WALLET_DEMOTED"
	TypeInsiderStreakBrokenAdmin    = "INSIDER_STREAK_BROKEN"
	TypeSharkFalsePositiveCorrected = "SHARK_FALSE_POSITIVE_CORRECTED"
	TypeLuckySpikeCandidate         = "LUCKY_SPIKE_CANDIDATE"
	TypeMLBLateGame                 = "MLB_LATE_GAME"
)

// DiscoveryAlert is the admin-channel payload describing a freshly promoted
// shark or insider-like candidate. Never user-facing.
type DiscoveryAlert struct {
	WalletShort   string
	WalletFull    string
	Pseudonym     string
	ProfileSlug   string
	Class         string // shark | insider_like
	Status        string // active | watch_only
	PromotionPath string
	Score         int
	Confidence    float64
	Severity      string // ADMIN | INFO

	// Shark stats (nullable — formatter prints n/a when fields are 0).
	ClosedPositions    int
	WinRate            float64 // legacy alias of ProfitableExitRate
	ROI                float64
	AvgClosedStake     float64
	RealizedPnL        float64
	HistoricalPnLKnown bool
	DataQuality        string

	// v4.1 realized trading evidence.
	ScoringBasis         string
	RealizedCycles       int
	ProfitableExitRate   float64 // NEVER outcome-correctness
	RealizedAvgROI       float64
	RealizedAvgNotional  float64
	RealizedTotalPnL     float64
	RealizedProfitFactor float64

	// v4.2 full discovery evidence — Performance + Sample sections.
	ProfitableCount     int
	LosingCount         int
	BreakevenCount      int
	AvgTradeNotional    float64 // avg entry notional across realized cycles
	MedianTradeNotional float64
	AvgWinUSD           float64
	MedianWinUSD        float64
	AvgLossUSD          float64 // negative value
	MedianLossUSD       float64 // negative value
	MaxWinUSD           float64
	MaxLossUSD          float64 // negative value
	GrossProfitUSD      float64
	GrossLossUSD        float64 // negative value
	EvaluatedFrom       string  // YYYY-MM-DD; empty → "n/a"
	EvaluatedTo         string  // YYYY-MM-DD; empty → "n/a"
	HistoryDays         int
	TradesChecked       int
	PositionsChecked    int
	RealizedCyclesCheck int
	OpenUnresolvedCount int
	LastTradeAt         string
	LastProfitableAt    string

	// Insider stats.
	LifetimeTrades int
	ClosedWins     int
	ClosedLosses   int
	StreakState    string
	LatestBetUSD   float64
	LatestBetOdds  float64
	FirstTradeAt   string

	ReasonHumanized string
	ReasonCodes     []string

	MarketSlug  string
	MarketTitle string
	EventSlug   string

	DedupKey     string
	DashboardURL string

	// Veto / false-positive correction fields. Populated when a promotion veto
	// fired or when an active wallet is demoted due to contradictory evidence.
	VetoReason              string  // e.g. "MASSIVE_NEGATIVE_ALL_TIME_PNL"
	VetoProfileCashPnL      float64 // profile all-time cashPnL (realized + unrealized)
	VetoProfileCashPnLKnown bool
	VetoLocalPnL            float64 // what local scoring said (positive)
	VetoOpenPositionRatio   float64 // open_count / total_count
	VetoPositionsChecked    int
	VetoOpenUnresolved      int
}

// LuckySpikeAlert is an admin-only candidate alert for suspicious
// high-frequency + high-profit behavior.
type LuckySpikeAlert struct {
	WalletShort string
	WalletFull  string
	Pseudonym   string
	ProfileSlug string

	Score      int
	Confidence float64

	WeeklyTradeCount       int
	WeeklyDistinctMarkets  int
	WeeklyRealizedCycles   int
	WeeklyProfitableCycles int
	WeeklyLosingCycles     int
	WeeklyProfitPct        float64
	MonthlyProfitPct       float64
	MonthlyTradeCount      int
	MonthlyRealizedCycles  int

	WeeklyCoverageHours            float64
	AvgTradeIntervalMinutes        float64
	MonthlyAvgTradeIntervalMinutes float64
	AvgWeeklyTradeNotionalUSD      float64
	TradeHistoryPartialHint        string
	DataQuality                    string
	ReasonHumanized                string
	ReasonCodes                    []string
	DedupKey                       string
	DashboardURL                   string
}

// MLBLateGameAlert is an admin-only market timing alert for baseball games
// where the away team is batting in the top 9th/extra innings while trailing.
type MLBLateGameAlert struct {
	GamePK int

	AwayTeam  string
	HomeTeam  string
	AwayScore int
	HomeScore int
	Deficit   int

	Inning      int
	InningHalf  string
	InningState string
	Status      string
	GameTime    time.Time

	MarketSlug     string
	MarketTitle    string
	EventSlug      string
	EventTitle     string
	MatchedMarkets []MLBMatchedMarket

	ReasonCodes []string
	DedupKey    string
}

type MLBMatchedMarket struct {
	Slug       string
	Title      string
	EventSlug  string
	EventTitle string
}

// SharkBet is the payload for the SHARK NEW BET alert.
type SharkBet struct {
	WalletShort   string
	WalletFull    string // full 0x… address; required for clickable trader link
	Pseudonym     string
	ProfileSlug   string
	Class         string // "shark"
	Score         int
	Confidence    float64
	TotalTrades   int
	WinRate       float64
	RealizedPnL   float64
	RealizedKnown bool

	// v4 historical evidence — sourced from wallet_closed_position_latest.
	ClosedPositionsCount int
	HistoricalWinRate    float64
	HistoricalROI        float64
	AvgClosedStake       float64
	HistoricalPnL        float64
	HistoricalPnLKnown   bool
	PromotionPath        string

	// v4.1 realized trading evidence (preferred when sample sufficient).
	// "Profitable exit rate" = realized cycles with positive PnL ÷ total
	// realized cycles. NEVER outcome-correctness.
	ScoringBasis         string // realized_trading_pnl | api_closed_positions | proxy_partial
	RealizedCycles       int
	ProfitableExitRate   float64
	RealizedAvgROI       float64
	RealizedAvgNotional  float64
	RealizedTotalPnL     float64
	RealizedProfitFactor float64

	MarketSlug  string
	MarketTitle string
	EventSlug   string

	Direction Direction
	Outcome   string
	Side      string
	Notional  float64
	Price     float64
	Odds      float64
	Payoff    float64
	// ProfitIfWin = Payoff - Notional = Notional * (Odds - 1). Zero when not computed.
	ProfitIfWin float64

	// Alert gate fields: populated when the profit-tier gate was evaluated.
	AlertGateTier   string // e.g. "medium"
	AlertGateReason string // e.g. "medium-tier profit gate passed / odds x5.56 >= x4 / profit $10.9k >= $15k"

	ReasonHumanized string
	ReasonCodes     []string

	DedupKey string

	DashboardURL string
}

// InsiderBet — payload for INSIDER IS IN THE HOUSE alert.
type InsiderBet struct {
	WalletShort     string
	WalletFull      string // full 0x… address; required for clickable trader link
	ProfileSlug     string
	LifetimeTrades  int
	LifetimeMarkets int

	// v4 streak fields.
	StreakState  string // "clean" | "continues" | "broken"
	ClosedWins   int
	ClosedLosses int
	FirstTradeAt string

	MarketSlug  string
	MarketTitle string
	EventSlug   string

	Direction        Direction
	DirectionOutcome string // non-empty for categorical (OUTCOME_BUY/SELL) bets
	Notional         float64
	Price            float64
	Odds             float64
	Payoff           float64
	// ProfitIfWin = Payoff - Notional = Notional * (Odds - 1). Zero when not computed.
	ProfitIfWin float64

	ReasonHumanized string
	ReasonCodes     []string

	Severity     string
	DedupKey     string
	DashboardURL string
}

// ClusterAlert — payload for CLUSTER BET DETECTED.
type ClusterAlert struct {
	Direction        Direction
	DirectionOutcome string // non-empty for categorical (OUTCOME_BUY/SELL) clusters
	MarketSlug       string
	MarketTitle      string
	EventSlug        string
	EventTitle       string
	TotalNotional    float64
	WalletCount      int
	WeightedPrice    float64
	AverageOdds      float64
	PayoffIfWin      float64
	WindowSeconds    int
	Traders          []ClusterTrader // sorted
	ReasonCodes      []string
	DedupKey         string
	DashboardURL     string
}

type ClusterTrader struct {
	WalletShort string
	WalletFull  string // full 0x… address
	Pseudonym   string
	ProfileSlug string
	Class       string
	Score       int
	Notional    float64
}

// BurstAlert — payload for SHARK BURST DETECTED (single-trader cluster).
// Aggregates multiple entries of the same watched wallet within a short
// window so a dust spam does not produce N separate alerts.
type BurstAlert struct {
	WalletShort     string
	WalletFull      string // full 0x… address
	Pseudonym       string
	ProfileSlug     string
	Class           string // shark | insider_like
	BetsCount       int
	DistinctMarkets int
	TotalNotional   float64
	WeightedPrice   float64
	WindowMinutes   int
	Markets         []BurstMarketLine
	ReasonCodes     []string
	DedupKey        string
}

type BurstMarketLine struct {
	Slug      string
	Title     string
	Direction Direction
	Notional  float64
	BetCount  int
}

// ExitAlert — payload for POSITION EXIT.
type ExitAlert struct {
	WalletShort   string
	WalletFull    string // full 0x… address
	Pseudonym     string
	ProfileSlug   string
	Class         string // "shark" | "insider_like"
	MarketSlug    string
	MarketTitle   string
	EventSlug     string
	Outcome       string // "Yes"/"No"
	OpenedDir     Direction
	EntryPrice    float64
	EntryNotional float64
	ExitPrice     float64
	ExitNotional  float64
	Status        string // "partially_exited" | "closed"
	PnLEstimate   float64
	PnLKnown      bool
	HeldDuration  string // "2h 15m"
	Severity      string
	ReasonCodes   []string
	MissingData   []string
	DedupKey      string
}

// NewsAlert — payload for POLYMARKET EVENT NEWS.
type NewsAlert struct {
	EventTitle string
	EventSlug  string
	Title      string
	Summary    string
	SourceURL  string
	Time       time.Time
	DedupKey   string
}

// Direction mirrors walletintel.Direction to avoid an import cycle from
// the workers wiring layer. Use strings equal to YES_BUY etc.
type Direction string

func (d Direction) isCategorical() bool {
	return d == "OUTCOME_BUY" || d == "OUTCOME_SELL"
}
