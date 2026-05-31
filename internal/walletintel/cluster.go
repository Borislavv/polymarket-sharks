package walletintel

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"time"
)

// WatchedBet is the input to cluster detection. Built from watched_bets rows.
type WatchedBet struct {
	ID           string
	WalletID     string
	WalletClass  string // "shark" / "insider_like"
	WalletScore  int
	MarketID     string
	EventID      string
	MarketSlug   string
	EventSlug    string
	MarketTitle  string
	EventTitle   string
	Direction    Direction
	Outcome      string
	Side         string
	Notional     float64
	Price        float64
	Timestamp    time.Time
	HighImpact   bool
	NearCatalyst bool
}

// ClusterParams — config knobs.
type ClusterParams struct {
	WindowBefore     time.Duration
	WindowAfter      time.Duration
	MinWallets       int
	MinTotalNotional float64
	MinQualityScore  int
}

// ClusterResult is one detected cluster ready to be persisted/alerted.
type ClusterResult struct {
	MarketID         string
	EventID          string
	MarketSlug       string
	MarketTitle      string
	EventSlug        string
	EventTitle       string
	Direction        Direction
	DirectionOutcome string // non-empty for categorical (OUTCOME_BUY/SELL) clusters
	WindowStart      time.Time
	WindowEnd        time.Time
	WalletCount      int
	TotalNotional    float64
	WeightedPrice    float64
	AverageOdds      float64
	PayoffIfWinTotal float64
	ClusterScore     int
	ReasonCodes      []string
	FeatureSnapshot  FeatureSnapshot
	WatchedBetIDs    []string
	WalletIDs        []string
	DedupKey         string
}

// FindClusters scans only watched bets (the input slice is expected to be
// pre-filtered to watchlisted wallets) and emits clusters that meet the
// configured thresholds. Deterministic ordering and stable dedup keys.
func FindClusters(bets []WatchedBet, p ClusterParams) []ClusterResult {
	if p.WindowBefore <= 0 {
		p.WindowBefore = 3 * time.Hour
	}
	if p.WindowAfter <= 0 {
		p.WindowAfter = 3 * time.Hour
	}
	if p.MinWallets < 2 {
		p.MinWallets = 2
	}
	window := p.WindowBefore + p.WindowAfter

	// Group by (market_id, direction, direction_outcome).
	// DirectionOutcome distinguishes different categorical outcomes within the
	// same market so "CHICAGO WHITE SOX BUY" and "MIAMI MARLINS BUY" are not
	// merged into one cluster.
	type key struct {
		MarketID         string
		Direction        Direction
		DirectionOutcome string
	}
	groups := map[key][]WatchedBet{}
	for _, b := range bets {
		k := key{MarketID: b.MarketID, Direction: b.Direction, DirectionOutcome: b.Outcome}
		groups[k] = append(groups[k], b)
	}

	var results []ClusterResult
	for k, group := range groups {
		// sort by time ascending
		sort.Slice(group, func(i, j int) bool { return group[i].Timestamp.Before(group[j].Timestamp) })

		// scan sliding window using each bet as anchor; pick non-overlapping
		seen := map[string]bool{} // dedup within group
		for i := 0; i < len(group); i++ {
			start := group[i].Timestamp.Add(-p.WindowBefore)
			end := group[i].Timestamp.Add(p.WindowAfter)
			var win []WatchedBet
			walletSet := map[string]bool{}
			for _, b := range group {
				if !b.Timestamp.Before(start) && !b.Timestamp.After(end) {
					win = append(win, b)
					walletSet[b.WalletID] = true
				}
			}
			if len(walletSet) < p.MinWallets {
				continue
			}
			tot := 0.0
			for _, b := range win {
				tot += b.Notional
			}
			if tot < p.MinTotalNotional {
				continue
			}
			cr := buildClusterResult(k.MarketID, k.Direction, k.DirectionOutcome, win, walletSet, start, end, window)
			if cr.ClusterScore < p.MinQualityScore {
				continue
			}
			if seen[cr.DedupKey] {
				continue
			}
			seen[cr.DedupKey] = true
			results = append(results, cr)
		}
	}
	// stable order
	sort.Slice(results, func(i, j int) bool {
		if results[i].MarketID != results[j].MarketID {
			return results[i].MarketID < results[j].MarketID
		}
		return results[i].DedupKey < results[j].DedupKey
	})
	return results
}

func buildClusterResult(marketID string, dir Direction, dirOutcome string, win []WatchedBet, walletSet map[string]bool, start, end time.Time, window time.Duration) ClusterResult {
	walletIDs := make([]string, 0, len(walletSet))
	for w := range walletSet {
		walletIDs = append(walletIDs, w)
	}
	sort.Strings(walletIDs)

	betIDs := make([]string, 0, len(win))
	totalNotional := 0.0
	weightedPriceNum := 0.0
	payoff := 0.0
	hasHighImpact := false
	hasCatalyst := false
	classes := map[string]struct{}{}
	for _, b := range win {
		betIDs = append(betIDs, b.ID)
		totalNotional += b.Notional
		weightedPriceNum += b.Price * b.Notional
		if b.Price > 0 && b.Price <= 1 {
			payoff += b.Notional / b.Price
		}
		if b.HighImpact {
			hasHighImpact = true
		}
		if b.NearCatalyst {
			hasCatalyst = true
		}
		if b.WalletClass != "" {
			classes[b.WalletClass] = struct{}{}
		}
	}
	sort.Strings(betIDs)
	weightedPrice := 0.0
	if totalNotional > 0 {
		weightedPrice = weightedPriceNum / totalNotional
	}
	avgOdds := 0.0
	if weightedPrice > 0 {
		avgOdds = 1.0 / weightedPrice
	}

	// Cluster scoring
	walletCntScore := clusterWalletCountScore(len(walletSet))    // 0..25
	notionalScore := clusterNotionalScore(totalNotional)         // 0..25
	qualityScore := clusterWalletQualityScore(win)               // 0..15
	dirAlignScore := 15                                          // by construction all match dir
	compressionScore := clusterTimeCompressionScore(win, window) // 0..10
	catalystScore := 0
	if hasCatalyst {
		catalystScore = 5
	}
	liquidityCtx := 5
	rulesPenalty := 0
	score := walletCntScore + notionalScore + qualityScore + dirAlignScore + compressionScore + catalystScore + liquidityCtx - rulesPenalty
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	var reasons []string
	if len(walletSet) >= 3 {
		reasons = append(reasons, "MULTI_WALLET_ALIGNED")
	}
	if hasHighImpact {
		reasons = append(reasons, "HIGH_IMPACT_MARKET")
	}
	if hasCatalyst {
		reasons = append(reasons, "NEAR_CATALYST")
	}
	if _, ok := classes["insider_like"]; ok {
		reasons = append(reasons, "INSIDER_LIKE_PARTICIPANT")
	}
	if _, ok := classes["shark"]; ok {
		reasons = append(reasons, "SHARK_PARTICIPANT")
	}

	bucket := bucketTime(start, end)
	dedup := clusterDedupKey(marketID, dir, dirOutcome, bucket, walletIDs)

	snap := FeatureSnapshot{
		"wallet_count_score":        walletCntScore,
		"total_notional_score":      notionalScore,
		"wallet_quality_score":      qualityScore,
		"direction_alignment_score": dirAlignScore,
		"time_compression_score":    compressionScore,
		"catalyst_proximity_score":  catalystScore,
		"liquidity_context_score":   liquidityCtx,
		"rules_risk_penalty":        rulesPenalty,
		"wallet_count":              len(walletSet),
		"total_notional":            totalNotional,
		"weighted_price":            weightedPrice,
		"avg_odds":                  avgOdds,
		"payoff_if_win_total":       payoff,
		"window_seconds":            window.Seconds(),
		"high_impact":               hasHighImpact,
		"near_catalyst":             hasCatalyst,
	}

	marketSlug, marketTitle, eventID, eventSlug, eventTitle := "", "", "", "", ""
	if len(win) > 0 {
		marketSlug = win[0].MarketSlug
		marketTitle = win[0].MarketTitle
		eventID = win[0].EventID
		eventSlug = win[0].EventSlug
		eventTitle = win[0].EventTitle
	}

	return ClusterResult{
		MarketID:         marketID,
		EventID:          eventID,
		MarketSlug:       marketSlug,
		MarketTitle:      marketTitle,
		EventSlug:        eventSlug,
		EventTitle:       eventTitle,
		Direction:        dir,
		DirectionOutcome: dirOutcome,
		WindowStart:      start,
		WindowEnd:        end,
		WalletCount:      len(walletSet),
		TotalNotional:    totalNotional,
		WeightedPrice:    weightedPrice,
		AverageOdds:      avgOdds,
		PayoffIfWinTotal: payoff,
		ClusterScore:     score,
		ReasonCodes:      reasons,
		FeatureSnapshot:  snap,
		WatchedBetIDs:    betIDs,
		WalletIDs:        walletIDs,
		DedupKey:         dedup,
	}
}

func clusterWalletCountScore(n int) int {
	switch {
	case n >= 5:
		return 25
	case n == 4:
		return 20
	case n == 3:
		return 15
	case n == 2:
		return 10
	}
	return 0
}

func clusterNotionalScore(tot float64) int {
	if tot <= 0 {
		return 0
	}
	// log scale: $5k → 5; $20k → 15; $100k+ → 25
	v := math.Round((math.Log10(tot) - 2.5) * 12.5)
	if v < 0 {
		v = 0
	}
	if v > 25 {
		v = 25
	}
	return int(v)
}

func clusterWalletQualityScore(win []WatchedBet) int {
	if len(win) == 0 {
		return 0
	}
	sum := 0
	for _, b := range win {
		sum += b.WalletScore
	}
	avg := sum / len(win)
	v := (avg * 15) / 100
	if v > 15 {
		v = 15
	}
	if v < 0 {
		v = 0
	}
	return v
}

func clusterTimeCompressionScore(win []WatchedBet, window time.Duration) int {
	if len(win) < 2 || window <= 0 {
		return 0
	}
	minT, maxT := win[0].Timestamp, win[0].Timestamp
	for _, b := range win {
		if b.Timestamp.Before(minT) {
			minT = b.Timestamp
		}
		if b.Timestamp.After(maxT) {
			maxT = b.Timestamp
		}
	}
	span := maxT.Sub(minT)
	if span <= 0 {
		return 10
	}
	ratio := float64(span) / float64(window)
	v := int(math.Round((1 - ratio) * 10))
	if v < 0 {
		v = 0
	}
	if v > 10 {
		v = 10
	}
	return v
}

// bucketTime rounds the window centre to a 30-minute bucket to make dedup
// stable across overlapping anchor scans within the same true cluster.
func bucketTime(start, end time.Time) time.Time {
	mid := start.Add(end.Sub(start) / 2).UTC()
	return mid.Truncate(30 * time.Minute)
}

// clusterDedupKey is deterministic for the same cluster regardless of bet
// order. sha256(alert_type | market_id | direction | direction_outcome | bucket | sortedWalletIDs).
// direction_outcome is included so two categorical clusters on different outcomes
// in the same market produce distinct keys.
func clusterDedupKey(marketID string, dir Direction, dirOutcome string, bucket time.Time, sortedWalletIDs []string) string {
	h := sha256.New()
	h.Write([]byte("CLUSTER_BET|"))
	h.Write([]byte(marketID))
	h.Write([]byte("|"))
	h.Write([]byte(string(dir)))
	h.Write([]byte("|"))
	h.Write([]byte(dirOutcome))
	h.Write([]byte("|"))
	h.Write([]byte(bucket.Format(time.RFC3339)))
	h.Write([]byte("|"))
	for _, w := range sortedWalletIDs {
		h.Write([]byte(w))
		h.Write([]byte(","))
	}
	return hex.EncodeToString(h.Sum(nil))
}
