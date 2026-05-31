package postgres

import (
	"context"
	"encoding/json"
	_ "encoding/json" // explicit: SharkEvidence parses feature_snapshot JSON
)

// ScoreRow is the storage-level representation of a wallet score. Workers
// translate from walletintel.ScoreResult to ScoreRow to avoid a cycle.
type ScoreRow struct {
	Strategy        string
	Class           string
	Score           int
	Confidence      float64
	Promote         bool
	ScoreVersion    string
	FeatureSnapshot map[string]any
	ReasonCodes     []string
	MissingData     []string
}

// InsertScore persists a score row. Always called — even rejected scores
// are stored for audit. Returns the row id.
func (s *Store) InsertScore(ctx context.Context, walletID string, r ScoreRow) (string, error) {
	snap, err := json.Marshal(r.FeatureSnapshot)
	if err != nil {
		return "", err
	}
	const q = `
		INSERT INTO wallet_scores
		    (wallet_id, strategy, class, score, confidence, promote,
		     score_version, feature_snapshot, reason_codes, missing_data)
		VALUES
		    ($1::uuid, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)
		RETURNING id::text`
	var id string
	if err := s.Pool.QueryRow(ctx, q,
		walletID, r.Strategy, r.Class, r.Score, r.Confidence, r.Promote,
		r.ScoreVersion, string(snap), reasonsArr(r.ReasonCodes), reasonsArr(r.MissingData),
	).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

// WatchlistRow is the storage-level promotion record.
type WatchlistRow struct {
	WalletID        string
	Class           string
	Status          string
	Score           int
	Confidence      float64
	ReasonCodes     []string
	FeatureSnapshot map[string]any
	ScoreVersion    string
}

// SharkScoreRow is the minimal projection of the latest wallet_scores row
// for a shark strategy. Used by ReconcileFailedSharks as the authoritative
// source of truth instead of the watchlist's arbitration feature_snapshot.
type SharkScoreRow struct {
	Promote      bool
	Score        int
	ScoreVersion string
	ReasonCodes  []string
	Found        bool
}

// GetLatestSharkScoreRow returns the most-recent wallet_scores entry with
// strategy='shark_score' for the given walletID. Found=false when no row
// exists. Never returns an error on "not found" — the caller must inspect Found.
func (s *Store) GetLatestSharkScoreRow(ctx context.Context, walletID string) (SharkScoreRow, error) {
	var r SharkScoreRow
	var reasons []string
	err := s.Pool.QueryRow(ctx, `
		SELECT promote, score, score_version, COALESCE(reason_codes, '{}')
		FROM wallet_scores
		WHERE wallet_id = $1::uuid AND strategy = 'shark_score'
		ORDER BY calculated_at DESC LIMIT 1
	`, walletID).Scan(&r.Promote, &r.Score, &r.ScoreVersion, &reasons)
	if err != nil {
		return r, nil // not found
	}
	r.Found = true
	r.ReasonCodes = reasons
	return r, nil
}

func (s *Store) UpsertWatchlist(ctx context.Context, p WatchlistRow) error {
	snap, err := json.Marshal(p.FeatureSnapshot)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO wallet_watchlist
		    (wallet_id, class, status, score, confidence, reason_codes,
		     feature_snapshot, score_version, promoted_at, updated_at)
		VALUES
		    ($1::uuid, $2, $3, $4, $5, $6, $7::jsonb, $8, now(), now())
		ON CONFLICT (wallet_id) DO UPDATE SET
		    class            = EXCLUDED.class,
		    status           = EXCLUDED.status,
		    score            = EXCLUDED.score,
		    confidence       = EXCLUDED.confidence,
		    reason_codes     = EXCLUDED.reason_codes,
		    feature_snapshot = EXCLUDED.feature_snapshot,
		    score_version    = EXCLUDED.score_version,
		    updated_at       = now()`
	_, err = s.Pool.Exec(ctx, q,
		p.WalletID, p.Class, p.Status, p.Score, p.Confidence,
		reasonsArr(p.ReasonCodes), string(snap), p.ScoreVersion,
	)
	return err
}

// SharkEvidence is the projection of the latest shark_score row for a
// wallet — used by AlertRouter callers to render real evidence (no fake
// zeros) into NEW BET messages.
type SharkEvidence struct {
	Found            bool
	Score            int
	Confidence       float64
	TotalTrades      int
	RealizedPnL      float64
	RealizedPnLKnown bool
	WinRate          float64
	WinRateKnown     bool
	ReasonCodes      []string
	ScoreVersion     string

	// v4 historical evidence projected from feature_snapshot.
	ClosedPositionsCount int
	HistoricalWinRate    float64
	HistoricalROI        float64
	AvgClosedStake       float64
	HistoricalPnL        float64
	HistoricalPnLKnown   bool
	PromotionPath        string

	// v4.1 realized trading evidence.
	ScoringBasis         string
	RealizedCycles       int
	ProfitableExitRate   float64
	RealizedAvgROI       float64
	RealizedAvgNotional  float64
	RealizedTotalPnL     float64
	RealizedProfitFactor float64
}

// GetLatestSharkEvidence returns the most recent shark_score row plus
// useful evidence extracted from feature_snapshot. Found=false means no
// shark row exists yet for this wallet.
func (s *Store) GetLatestSharkEvidence(ctx context.Context, walletID string) (SharkEvidence, error) {
	var ev SharkEvidence
	var fs []byte
	var reasons []string
	err := s.Pool.QueryRow(ctx, `
		SELECT score, confidence, feature_snapshot::text, reason_codes, score_version
		FROM wallet_scores
		WHERE wallet_id = $1::uuid AND strategy = 'shark_score'
		ORDER BY calculated_at DESC LIMIT 1
	`, walletID).Scan(&ev.Score, &ev.Confidence, &fs, &reasons, &ev.ScoreVersion)
	if err != nil {
		return ev, nil // not found → empty zero-value, no error to caller
	}
	ev.Found = true
	ev.ReasonCodes = reasons
	// pluck a couple of fields from the snapshot. Best-effort json walk.
	var snap map[string]any
	_ = json.Unmarshal(fs, &snap)
	if v, ok := snap["total_trades"].(float64); ok {
		ev.TotalTrades = int(v)
	}
	if v, ok := snap["realized_pnl"].(float64); ok {
		ev.RealizedPnL = v
	}
	if v, ok := snap["realized_pnl_known"].(bool); ok {
		ev.RealizedPnLKnown = v
	}
	if v, ok := snap["win_rate"].(float64); ok {
		ev.WinRate = v
		ev.WinRateKnown = true
		ev.HistoricalWinRate = v
	}
	if v, ok := snap["closed_positions_count"].(float64); ok {
		ev.ClosedPositionsCount = int(v)
	}
	if v, ok := snap["roi"].(float64); ok {
		ev.HistoricalROI = v
	}
	if v, ok := snap["avg_closed_position_stake"].(float64); ok {
		ev.AvgClosedStake = v
	}
	if v, ok := snap["realized_pnl"].(float64); ok {
		ev.HistoricalPnL = v
		ev.HistoricalPnLKnown = true
	}
	if v, ok := snap["promotion_path"].(string); ok {
		ev.PromotionPath = v
	}
	if v, ok := snap["scoring_basis"].(string); ok {
		ev.ScoringBasis = v
	}
	if v, ok := snap["realized_cycles_count"].(float64); ok {
		ev.RealizedCycles = int(v)
	}
	if v, ok := snap["profitable_exit_rate"].(float64); ok {
		ev.ProfitableExitRate = v
	}
	if v, ok := snap["realized_avg_roi"].(float64); ok {
		ev.RealizedAvgROI = v
	}
	if v, ok := snap["realized_avg_notional"].(float64); ok {
		ev.RealizedAvgNotional = v
	}
	if v, ok := snap["realized_total_pnl"].(float64); ok {
		ev.RealizedTotalPnL = v
	}
	if v, ok := snap["realized_profit_factor"].(float64); ok {
		ev.RealizedProfitFactor = v
	}
	return ev, nil
}

type WatchedWallet struct {
	ID           string
	ProxyWallet  string
	Class        string
	Score        int
	Confidence   float64
	Status       string
	Pseudonym    string
	ProfileSlug  string
	ScoreVersion string
}

// GetWatchlistRow returns the current watchlist row for a wallet (if any).
// Used by the discovery emitter to detect status transitions.
func (s *Store) GetWatchlistRow(ctx context.Context, walletID string) (WatchedWallet, bool, error) {
	const q = `
		SELECT w.id::text, w.proxy_wallet, ww.class, ww.score, COALESCE(ww.confidence,0),
		       ww.status, COALESCE(w.pseudonym,''), COALESCE(w.profile_slug,''),
		       COALESCE(ww.score_version,'')
		FROM wallet_watchlist ww
		JOIN wallets w ON w.id = ww.wallet_id
		WHERE ww.wallet_id = $1::uuid`
	var w WatchedWallet
	err := s.Pool.QueryRow(ctx, q, walletID).Scan(&w.ID, &w.ProxyWallet, &w.Class, &w.Score, &w.Confidence,
		&w.Status, &w.Pseudonym, &w.ProfileSlug, &w.ScoreVersion)
	if err != nil {
		return WatchedWallet{}, false, nil
	}
	return w, true, nil
}

// ListActiveWatchlist returns wallets currently being monitored.
func (s *Store) ListActiveWatchlist(ctx context.Context) ([]WatchedWallet, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT w.id::text, w.proxy_wallet, ww.class, ww.score, COALESCE(ww.confidence,0),
		       ww.status, COALESCE(w.pseudonym,''), COALESCE(w.profile_slug,''),
		       COALESCE(ww.score_version,'')
		FROM wallet_watchlist ww
		JOIN wallets w ON w.id = ww.wallet_id
		WHERE ww.status IN ('active','watch_only')
		ORDER BY ww.score DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchedWallet
	for rows.Next() {
		var w WatchedWallet
		if err := rows.Scan(&w.ID, &w.ProxyWallet, &w.Class, &w.Score, &w.Confidence,
			&w.Status, &w.Pseudonym, &w.ProfileSlug, &w.ScoreVersion); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func reasonsArr(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}
