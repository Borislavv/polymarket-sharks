package walletintel

import "github.com/Borislavv/polymarket-sharks/internal/storage/postgres"

// toScoreRow translates the in-memory ScoreResult to the storage row type.
// Kept in walletintel because postgres must not depend on this package.
func toScoreRow(r ScoreResult) postgres.ScoreRow {
	return postgres.ScoreRow{
		Strategy:        r.Strategy,
		Class:           r.Class,
		Score:           r.Score,
		Confidence:      r.Confidence,
		Promote:         r.Promote,
		ScoreVersion:    r.ScoreVersion,
		FeatureSnapshot: map[string]any(r.FeatureSnapshot),
		ReasonCodes:     r.ReasonCodes,
		MissingData:     r.MissingData,
	}
}

func toWatchlistRow(p WatchlistPromotion) postgres.WatchlistRow {
	return postgres.WatchlistRow{
		WalletID:        p.WalletID,
		Class:           p.Class,
		Status:          p.Status,
		Score:           p.Score,
		Confidence:      p.Confidence,
		ReasonCodes:     p.ReasonCodes,
		FeatureSnapshot: map[string]any(p.FeatureSnapshot),
		ScoreVersion:    p.ScoreVersion,
	}
}

// ToClusterRow exposes the conversion for cluster workers (in this package).
func ToClusterRow(c ClusterResult) postgres.ClusterRow {
	return postgres.ClusterRow{
		MarketID:         c.MarketID,
		EventID:          c.EventID,
		Direction:        string(c.Direction),
		DirectionOutcome: c.DirectionOutcome,
		WindowStart:      c.WindowStart,
		WindowEnd:        c.WindowEnd,
		WalletCount:      c.WalletCount,
		TotalNotional:    c.TotalNotional,
		WeightedPrice:    c.WeightedPrice,
		AverageOdds:      c.AverageOdds,
		PayoffIfWinTotal: c.PayoffIfWinTotal,
		ClusterScore:     c.ClusterScore,
		WatchedBetIDs:    c.WatchedBetIDs,
		WalletIDs:        c.WalletIDs,
		ReasonCodes:      c.ReasonCodes,
		FeatureSnapshot:  map[string]any(c.FeatureSnapshot),
		DedupKey:         c.DedupKey,
	}
}
