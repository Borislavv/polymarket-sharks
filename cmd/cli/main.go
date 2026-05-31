// Command cli is a small admin tool for watchtower.
//
// Subcommands:
//
//	wallet-dry-run --wallet <0x..>
//	    Pull historical /trades + /closed-positions, persist them, then
//	    compute v4 shark + insider-like scores against the freshly-loaded
//	    evidence. Prints a single-screen report and exits. No alerts are
//	    sent. Useful for sanity-checking a single wallet before deploying
//	    new thresholds, or for validating that a specific wallet does/does
//	    not satisfy current gates.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/config"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/walletintel"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	switch sub {
	case "wallet-dry-run":
		if err := walletDryRun(); err != nil {
			fmt.Fprintln(os.Stderr, "wallet-dry-run:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: cli <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  wallet-dry-run --wallet <0x..>")
}

func walletDryRun() error {
	fs := flag.NewFlagSet("wallet-dry-run", flag.ContinueOnError)
	wallet := fs.String("wallet", "", "proxy wallet address (0x..)")
	dsn := fs.String("dsn", "", "database url (overrides DATABASE_URL)")
	dataBase := fs.String("data-api", "", "polymarket data-api base url (overrides POLYMARKET_DATA_API_BASE_URL)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *wallet == "" {
		fs.Usage()
		return fmt.Errorf("--wallet is required")
	}
	cfg, err := config.Load()
	if err != nil {
		// Fall back to env-only defaults when full config validation fails
		// (e.g. operator runs the CLI without all Telegram settings present).
		cfg = &config.Config{
			DatabaseURL:                   os.Getenv("DATABASE_URL"),
			PolymarketDataAPIBaseURL:      os.Getenv("POLYMARKET_DATA_API_BASE_URL"),
			HistoryBackfillTradePageSize:  500,
			HistoryBackfillClosedPageSize: 500,
			HistoryBackfillMaxTradePages:  20,
			HistoryBackfillMaxClosedPages: 20,
			SharkHistMinClosedPositions:   25,
			SharkHistMinROI:               0.10,
			SharkHistMinWinRate:           0.75,
			SharkHistMinAvgStakeUSD:       10_000,
			InsiderMaxLifetimeForCapture:  10,
			InsiderMinNotionalUSD:         20_000,
			InsiderMinOdds:                3.0,
			DataAPIRPSLimit:               8,
			HTTPTimeout:                   20 * time.Second,
			TargetCategories:              []string{"politics", "geopolitics", "war", "military", "elections"},
		}
	}
	if *dsn != "" {
		cfg.DatabaseURL = *dsn
	}
	if *dataBase != "" {
		cfg.PolymarketDataAPIBaseURL = *dataBase
	}
	if cfg.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL (or --dsn) is required")
	}
	if cfg.PolymarketDataAPIBaseURL == "" {
		cfg.PolymarketDataAPIBaseURL = "https://data-api.polymarket.com"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, "migrations"); err != nil {
		// migrations may already be applied by the long-running service —
		// not fatal for a read-mostly CLI.
		_ = err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	dataCli := dataapi.New(polymarket.New(cfg.PolymarketDataAPIBaseURL, cfg.DataAPIRPSLimit, cfg.HTTPTimeout))

	wid, err := store.UpsertWallet(ctx, postgres.Wallet{ProxyWallet: *wallet})
	if err != nil {
		return fmt.Errorf("upsert wallet: %w", err)
	}

	bf := &walletintel.HistoryBackfillWorker{
		DataAPI:        dataCli,
		Store:          store,
		Log:            log,
		BatchSize:      1,
		Concurrency:    1,
		TradePageSize:  cfg.HistoryBackfillTradePageSize,
		ClosedPageSize: cfg.HistoryBackfillClosedPageSize,
		MaxTradePages:  cfg.HistoryBackfillMaxTradePages,
		MaxClosedPages: cfg.HistoryBackfillMaxClosedPages,
	}
	bfStart := time.Now()
	bfRow, err := bf.BackfillOne(ctx, wid, *wallet)
	if err != nil {
		return fmt.Errorf("backfill: %w", err)
	}
	bfDur := time.Since(bfStart)

	runner := &walletintel.Runner{
		DataAPI: dataCli, Store: store, Log: log,
		SharkParams: walletintel.SharkParams{
			HistMinClosedPositions: cfg.SharkHistMinClosedPositions,
			HistMinROI:             cfg.SharkHistMinROI,
			HistMinWinRate:         cfg.SharkHistMinWinRate,
			HistMinAvgStakeUSD:     cfg.SharkHistMinAvgStakeUSD,
			MaxStaleDays:           cfg.SharkMaxStaleDays,
		},
		InsiderParams: walletintel.InsiderParams{
			MaxLifetimeForCapture: cfg.InsiderMaxLifetimeForCapture,
			MinNotionalUSD:        cfg.InsiderMinNotionalUSD,
			MinOdds:               cfg.InsiderMinOdds,
			HighImpactCategories:  cfg.TargetCategories,
		},
		TargetCategories: cfg.TargetCategories,
	}
	facts, err := runner.AssembleFacts(ctx, wid, *wallet)
	if err != nil {
		return fmt.Errorf("assemble facts: %w", err)
	}
	shark := walletintel.ScoreShark(facts, runner.SharkParams)
	insider := walletintel.ScoreInsiderLike(facts, runner.InsiderParams)
	stats, _ := store.GetHistoricalCloseStats(ctx, wid)

	fmt.Println("--- wallet dry-run report ---")
	fmt.Printf("wallet              : %s\n", *wallet)
	fmt.Printf("wallet_id           : %s\n", wid)
	fmt.Printf("backfill_duration   : %s\n", bfDur)
	fmt.Printf("trades_fetched      : %d (complete=%v)\n", bfRow.TradesFetched, bfRow.TradesComplete)
	fmt.Printf("closed_pos_fetched  : %d (complete=%v)\n", bfRow.ClosedPositionsFetched, bfRow.ClosedPositionsComplete)
	fmt.Printf("backfill_error      : %s\n", bfRow.LastError)
	fmt.Println()
	fmt.Println("== historical close stats ==")
	fmt.Printf("closed_positions    : %d (profitable=%d, losing=%d)\n", stats.ClosedCount, stats.ProfitableCount, stats.LosingCount)
	fmt.Printf("win_rate            : %.4f\n", stats.WinRate)
	fmt.Printf("ROI                 : %.4f\n", stats.ROI)
	fmt.Printf("avg_closed_stake    : %.2f\n", stats.AvgClosedStake)
	fmt.Printf("median_closed_stake : %.2f\n", stats.MedianClosedStake)
	fmt.Printf("realized_pnl        : %.2f\n", stats.TotalRealizedPnL)
	fmt.Printf("max_win/max_loss    : %.2f / %.2f\n", stats.MaxWin, stats.MaxLoss)
	if stats.LastClosedAt != nil {
		fmt.Printf("last_closed_at      : %s\n", stats.LastClosedAt.Format(time.RFC3339))
	}
	fmt.Println()
	fmt.Println("== insider lifetime ==")
	fmt.Printf("lifetime_trades     : %d\n", facts.LifetimeTradeCount)
	fmt.Printf("streak_clean        : %v (wins=%d losses=%d)\n", facts.InsiderStreakClean, facts.LifetimeProfitableCount, facts.LifetimeLosingCount)
	if !facts.LastTradeAt.IsZero() {
		fmt.Printf("last_trade_at       : %s\n", facts.LastTradeAt.Format(time.RFC3339))
	}
	fmt.Println()
	fmt.Println("== shark_score ==")
	printScore(shark)
	fmt.Println("== insider_like_score ==")
	printScore(insider)

	fmt.Println("== alerting ==")
	fmt.Printf("shark_alert_would_send   : %v (gated on shark promotion + entry side at runtime)\n", shark.Promote)
	fmt.Printf("insider_alert_would_send : %v (requires NEW_BET context in live worker)\n", insider.Promote)
	return nil
}

func printScore(r walletintel.ScoreResult) {
	fmt.Printf("strategy            : %s\n", r.Strategy)
	fmt.Printf("class               : %s\n", r.Class)
	fmt.Printf("score               : %d\n", r.Score)
	fmt.Printf("confidence          : %.2f\n", r.Confidence)
	fmt.Printf("promote             : %v\n", r.Promote)
	fmt.Printf("score_version       : %s\n", r.ScoreVersion)
	fmt.Printf("reason_codes        : %v\n", r.ReasonCodes)
	if len(r.MissingData) > 0 {
		fmt.Printf("missing_data        : %v\n", r.MissingData)
	}
	snap, _ := json.MarshalIndent(r.FeatureSnapshot, "", "  ")
	fmt.Printf("feature_snapshot    : %s\n", string(snap))
	fmt.Println()
}
