package postgres

import (
	"context"
	"time"
)

type Wallet struct {
	ID          string
	ProxyWallet string
	Pseudonym   string
	ProfileSlug string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// UpsertWallet inserts or refreshes a wallet row. Returns the wallet ID.
// Existing callers that don't care whether the row was newly inserted use
// this two-return form; callers that need to distinguish "new wallet
// discovered" from "wallet observed in market" should use UpsertWalletReturn.
func (s *Store) UpsertWallet(ctx context.Context, w Wallet) (string, error) {
	id, _, err := s.UpsertWalletReturn(ctx, w)
	return id, err
}

// UpsertWalletReturn inserts or refreshes a wallet row and reports whether
// the row was newly created (true) or already existed (false). Used by the
// holder scanner to emit accurate `wallet observed in market` vs
// `new wallet discovered` log lines.
func (s *Store) UpsertWalletReturn(ctx context.Context, w Wallet) (string, bool, error) {
	const q = `
		INSERT INTO wallets (proxy_wallet, pseudonym, profile_slug, last_seen_at)
		VALUES ($1, NULLIF($2,''), NULLIF($3,''), now())
		ON CONFLICT (proxy_wallet) DO UPDATE SET
		    pseudonym    = COALESCE(NULLIF(EXCLUDED.pseudonym,''),    wallets.pseudonym),
		    profile_slug = COALESCE(NULLIF(EXCLUDED.profile_slug,''), wallets.profile_slug),
		    last_seen_at = now()
		RETURNING id::text, (xmax = 0) AS inserted`
	var (
		id       string
		inserted bool
	)
	if err := s.Pool.QueryRow(ctx, q, w.ProxyWallet, w.Pseudonym, w.ProfileSlug).Scan(&id, &inserted); err != nil {
		return "", false, err
	}
	return id, inserted, nil
}
