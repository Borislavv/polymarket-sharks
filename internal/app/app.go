// Package app wires up storage, polymarket clients, workers, and the alert
// router into a single supervised process. Graceful shutdown is driven by
// ctx cancellation from cmd/watchtower.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/config"
	"github.com/Borislavv/polymarket-sharks/internal/marketscan"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/clob"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/dataapi"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/gamma"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket/nextjs"
	"github.com/Borislavv/polymarket-sharks/internal/retention"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
	"github.com/Borislavv/polymarket-sharks/internal/telegram"
	"github.com/Borislavv/polymarket-sharks/internal/walletintel"
)

type App struct {
	Cfg   *config.Config
	Log   *slog.Logger
	Store *postgres.Store

	Gamma    *gamma.Client
	Data     *dataapi.Client
	CLOBREST *clob.RESTClient
	CLOBWS   *clob.WSClient
	NextJS   *nextjs.Client

	Telegram *telegram.Client
	Router   *alerts.Router
	Links    alerts.LinkBuilder

	Runner  *walletintel.Runner
	Workers []Worker
}

type Worker interface {
	Run(ctx context.Context) error
}

// New constructs the full app. Migrations are applied. External
// connections are tested by ping in Store ctor.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	store, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(ctx, "migrations"); err != nil {
		return nil, err
	}
	if n, err := walletintel.ReconcileStaleSharks(ctx, store, log); err != nil {
		log.Warn("shark stale-version reconcile failed at startup", "err", err)
	} else if n > 0 {
		log.Info("stale-version sharks demoted at startup", "count", n)
	}
	startupParams := walletintel.SharkParams{
		QualifyingMinTrades:        cfg.SharkQualifyingMinTrades,
		QualifyingMinNotionalUSD:   cfg.SharkQualifyingMinNotionalUSD,
		QualifyingMinOdds:          cfg.SharkQualifyingMinOdds,
		QualifyingMinAvgNotional:   cfg.SharkQualifyingMinAvgNotional,
		EliteMinClosedPositions:    cfg.EliteMinClosedPositions,
		EliteMinWinRate:            cfg.EliteMinWinRate,
		EliteMinAvgEntryNotional:   cfg.EliteMinAvgEntryNotional,
		EliteMinTotalEntryNotional: cfg.EliteMinTotalEntryNotional,
		EliteMinROI:                cfg.EliteMinROI,
		EliteMinAvgOdds:            cfg.EliteMinAvgOdds,
		EliteMinPayoffRatio:        cfg.EliteMinPayoffRatio,
		HistMinClosedPositions:     cfg.SharkHistMinClosedPositions,
		HistMinROI:                 cfg.SharkHistMinROI,
		HistMinWinRate:             cfg.SharkHistMinWinRate,
		HistMinAvgStakeUSD:         cfg.SharkHistMinAvgStakeUSD,
	}
	// Restore sharks that were incorrectly demoted by the historical P0 bug
	// (reconcile used arbitration snapshot instead of scoring snapshot).
	if n, err := walletintel.RestoreIncorrectlyDemotedSharks(ctx, store, log); err != nil {
		log.Warn("unactual shark restore failed at startup", "err", err)
	} else if n > 0 {
		log.Info("incorrectly demoted sharks restored to active", "count", n)
	}
	if n, err := walletintel.ReconcileFailedSharks(ctx, store, startupParams, log); err != nil {
		log.Warn("shark hard-gate reconcile failed at startup", "err", err)
	} else if n > 0 {
		log.Info("failed-gate sharks demoted at startup", "count", n)
	}

	tokenCache := marketscan.NewTokenCache(store)
	gammaCli := gamma.New(polymarket.New(cfg.PolymarketGammaBaseURL, cfg.GammaRPSLimit, cfg.HTTPTimeout))
	dataCli := dataapi.New(polymarket.New(cfg.PolymarketDataAPIBaseURL, cfg.DataAPIRPSLimit, cfg.HTTPTimeout))
	clobREST := clob.NewREST(polymarket.New(cfg.PolymarketCLOBBaseURL, cfg.CLOBRPSLimit, cfg.HTTPTimeout))
	wsCli := clob.NewWS(cfg.PolymarketWSURL, log, 10*time.Second)
	wsConsumer := &marketscan.WSConsumer{Store: store, Cache: tokenCache, Log: log}
	wsCli.OnEvent(wsConsumer.HandleEvent)
	njCli := nextjs.New("https://polymarket.com", cfg.HTTPTimeout, cfg.NextJSBuildIDTTL)
	mlbCli := marketscan.NewMLBStatsClient(polymarket.New(cfg.MLBStatsAPIBaseURL, 2, cfg.HTTPTimeout))

	tg := telegram.New(cfg.TelegramBotToken, cfg.TelegramRPSLimit, cfg.HTTPTimeout)
	links := alerts.LinkBuilder{
		BaseEvent:     "https://polymarket.com/event",
		BaseMarket:    "https://polymarket.com/market",
		BaseProfile:   "https://polymarket.com/profile",
		BaseDashboard: cfg.InternalDashboardBaseURL,
	}
	router := alerts.NewRouter(store, tg, links, log,
		cfg.TelegramAdminChatID,
		cfg.TelegramBetsChatID,
		cfg.TelegramClustersChatID,
		cfg.TelegramNewsChatID,
	)
	router.AlertingEnabled = cfg.AlertingEnabled
	if !cfg.AlertingEnabled {
		log.Warn("alerting disabled — decisions persisted, telegram not called")
	}

	runner := &walletintel.Runner{
		DataAPI: dataCli,
		Store:   store,
		Log:     log,
		SharkParams: walletintel.SharkParams{
			MinTrades:                  cfg.SharkMinTrades,
			MinClosedPositions:         cfg.SharkMinClosedPositions,
			MinScore:                   cfg.SharkMinScore,
			MinConfidence:              cfg.SharkMinConfidence,
			MaxStaleDays:               cfg.SharkMaxStaleDays,
			QualifyingMinTrades:        cfg.SharkQualifyingMinTrades,
			QualifyingMinNotionalUSD:   cfg.SharkQualifyingMinNotionalUSD,
			QualifyingMinOdds:          cfg.SharkQualifyingMinOdds,
			QualifyingMinAvgNotional:   cfg.SharkQualifyingMinAvgNotional,
			EliteMinClosedPositions:    cfg.EliteMinClosedPositions,
			EliteMinWinRate:            cfg.EliteMinWinRate,
			EliteMinAvgEntryNotional:   cfg.EliteMinAvgEntryNotional,
			EliteMinTotalEntryNotional: cfg.EliteMinTotalEntryNotional,
			EliteMinROI:                cfg.EliteMinROI,
			EliteMinAvgOdds:            cfg.EliteMinAvgOdds,
			EliteMinPayoffRatio:        cfg.EliteMinPayoffRatio,
			HistMinClosedPositions:     cfg.SharkHistMinClosedPositions,
			HistMinROI:                 cfg.SharkHistMinROI,
			HistMinWinRate:             cfg.SharkHistMinWinRate,
			HistMinAvgStakeUSD:         cfg.SharkHistMinAvgStakeUSD,
			MaxAvgTradeInterval:        2 * time.Minute,
			MinWindowProfitPct:         0.30,
		},
		InsiderParams: walletintel.InsiderParams{
			MaxLifetimeTrades:     cfg.InsiderMaxLifetimeTrades,
			MaxLifetimeMarkets:    cfg.InsiderMaxLifetimeMarkets,
			MinNotionalUSD:        cfg.InsiderMinNotionalUSD,
			MinScore:              cfg.InsiderMinScore,
			MinConfidence:         cfg.InsiderMinConfidence,
			LowProbPriceThr:       cfg.InsiderLowProbPriceThr,
			MinOdds:               cfg.InsiderMinOdds,
			MaxLifetimeForCapture: cfg.InsiderMaxLifetimeForCapture,
			HighImpactCategories:  cfg.TargetCategories,
			MaxAvgTradeInterval:   2 * time.Minute,
			MinWindowProfitPct:    0.30,
		},
		TargetCategories: cfg.TargetCategories,
		Cooldown:         walletintel.NewScoreCooldown(15 * time.Minute),
		Discovery: &walletintel.DiscoveryEmitter{
			Store:  store,
			Router: router,
			Log:    log,
			Links:  links,
		},
	}

	a := &App{
		Cfg:      cfg,
		Log:      log,
		Store:    store,
		Gamma:    gammaCli,
		Data:     dataCli,
		CLOBREST: clobREST,
		CLOBWS:   wsCli,
		NextJS:   njCli,
		Telegram: tg,
		Router:   router,
		Links:    links,
		Runner:   runner,
	}

	// Build worker set
	a.Workers = []Worker{
		&marketscan.DiscoveryWorker{
			Gamma: gammaCli, Store: store, Log: log,
			Interval: cfg.DiscoveryInterval, TargetCategories: cfg.TargetCategories,
		},
		&marketscan.HotsetWorker{
			Store: store, WS: wsCli, Log: log,
			Interval: cfg.ClusterScanInterval, MaxMarkets: cfg.HotsetMaxMarkets,
		},
		&marketscan.HolderScanWorker{
			DataAPI: dataCli, Store: store, Log: log,
			Interval:     cfg.HolderScanInterval,
			HotsetSize:   cfg.HotsetMaxMarkets,
			DeepScanSize: cfg.HolderDeepScanSize,
			OnNewWallet: func(walletID, proxy string) {
				// Fire-and-forget scoring trigger; bounded by context.
				go func() {
					tctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					_, _ = runner.ScoreWallet(tctx, walletID, proxy, nil, walletintel.MarketRulesInputs{}, false, 0)
				}()
			},
		},
		&walletintel.HistoryBackfillWorker{
			DataAPI:        dataCli,
			Store:          store,
			Log:            log,
			Interval:       cfg.HistoryBackfillInterval,
			BatchSize:      cfg.HistoryBackfillBatchSize,
			Concurrency:    cfg.HistoryBackfillConcurrency,
			TradePageSize:  cfg.HistoryBackfillTradePageSize,
			ClosedPageSize: cfg.HistoryBackfillClosedPageSize,
			MaxTradePages:  cfg.HistoryBackfillMaxTradePages,
			MaxClosedPages: cfg.HistoryBackfillMaxClosedPages,
			SnapshotCache:  walletintel.NewClosedPositionSnapshotCache(),
			OnWalletBackfilled: func(walletID, proxy string) {
				// Backfill completion is a material new signal — bypass cooldown.
				go func() {
					tctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					_, _ = runner.ScoreWalletForce(tctx, walletID, proxy, nil, walletintel.MarketRulesInputs{}, false, 0)
				}()
			},
		},
		&walletintel.LargeTradeCaptureWorker{
			DataAPI:         dataCli,
			Store:           store,
			Router:          router,
			Log:             log,
			Links:           links,
			InsiderParams:   runner.InsiderParams,
			HotsetSize:      cfg.HolderDeepScanSize,
			TradesPerMarket: 100,
			LookbackTrades:  100,
			Interval:        cfg.ClusterScanInterval,
			Discovery:       runner.Discovery,
		},
		&walletintel.WatchedWalletWorker{
			DataAPI: dataCli, Store: store, Router: router, Log: log,
			Interval:                 cfg.WatchedWalletPollInterval,
			Runner:                   runner,
			Links:                    links,
			InsiderParams:            runner.InsiderParams,
			LifecycleEnabled:         cfg.LifecycleEnabled,
			ExitAlertsEnabled:        cfg.ExitAlertsEnabled,
			ExitFullCloseTolerance:   cfg.ExitFullCloseTolerance,
			SharkAlertMinNotionalUSD: cfg.SharkAlertMinNotionalUSD,
			ProfitGate: walletintel.ProfitGateParams{
				Enabled:           cfg.AlertProfitGateEnabled,
				TinyMaxNotional:   cfg.AlertTinyMaxNotionalUSD,
				TinyMinOdds:       cfg.AlertTinyMinOdds,
				TinyMinProfit:     cfg.AlertTinyMinProfitUSD,
				SmallMaxNotional:  cfg.AlertSmallMaxNotionalUSD,
				SmallMinOdds:      cfg.AlertSmallMinOdds,
				SmallMinProfit:    cfg.AlertSmallMinProfitUSD,
				MediumMaxNotional: cfg.AlertMediumMaxNotionalUSD,
				MediumMinOdds:     cfg.AlertMediumMinOdds,
				MediumMinProfit:   cfg.AlertMediumMinProfitUSD,
				LargeMaxNotional:  cfg.AlertLargeMaxNotionalUSD,
				LargeMinOdds:      cfg.AlertLargeMinOdds,
				LargeMinProfit:    cfg.AlertLargeMinProfitUSD,
				MegaMinNotional:   cfg.AlertMegaMinNotionalUSD,
				MegaMinOdds:       cfg.AlertMegaMinOdds,
				MegaMinProfit:     cfg.AlertMegaMinProfitUSD,
			},
		},
		&walletintel.ClusterWorker{
			Store:    store,
			Router:   router,
			Log:      log,
			Interval: cfg.ClusterScanInterval,
			Params: walletintel.ClusterParams{
				WindowBefore:     cfg.ClusterWindowBefore,
				WindowAfter:      cfg.ClusterWindowAfter,
				MinWallets:       cfg.ClusterMinWallets,
				MinTotalNotional: cfg.ClusterMinTotalNotional,
				MinQualityScore:  cfg.ClusterMinQualityScore,
			},
			Links:                 links,
			IncludeExits:          cfg.ExitClusterEnabled,
			ClusterMinTotalProfit: cfg.ClusterMinTotalProfitUSD,
			ClusterMinAvgOdds:     cfg.ClusterMinAvgOdds,
		},
		&walletintel.BurstWorker{
			Store:            store,
			Router:           router,
			Log:              log,
			Links:            links,
			Interval:         cfg.ClusterScanInterval,
			Window:           cfg.BurstWindow,
			MinBets:          cfg.BurstMinBets,
			MinMarkets:       cfg.BurstMinDistinctMarkets,
			MinTotalNotional: cfg.BurstMinTotalNotionalUSD,
		},
		&marketscan.NewsWorker{
			NextJS:   njCli,
			Store:    store,
			Router:   router,
			Log:      log,
			Interval: cfg.NewsScanInterval,
			Enabled:  cfg.NextJSNewsEnabled,
			Links:    links,
		},
		&marketscan.PriceSamplerWorker{
			CLOB:        clobREST,
			Store:       store,
			Log:         log,
			Interval:    cfg.PriceSamplerInterval,
			MaxPerCycle: cfg.PriceSamplerMaxPerCycle,
		},
		&alerts.RetryWorker{
			Store:       store,
			Telegram:    tg,
			Log:         log,
			Interval:    cfg.TelegramRetryInterval,
			MaxAttempts: cfg.TelegramMaxAttempts,
			BatchSize:   50,
		},
		&walletintel.ReconcileWorker{
			Store:    store,
			Params:   runner.SharkParams,
			Log:      log,
			Interval: 10 * time.Minute,
		},
	}
	if cfg.LuckySpikeEnabled {
		a.Workers = append(a.Workers, &walletintel.LuckySpikeWorker{
			DataAPI:                  dataCli,
			Store:                    store,
			Router:                   router,
			Log:                      log,
			Links:                    links,
			Interval:                 cfg.LuckySpikeInterval,
			MaxMarkets:               cfg.LuckySpikeMaxMarkets,
			MarketTradesLimit:        cfg.LuckySpikeMarketTradesLimit,
			MarketConcurrency:        cfg.LuckySpikeMarketConcurrency,
			CandidateTradePageSize:   cfg.LuckySpikeCandidateTradePageSize,
			CandidateTradeMaxPages:   cfg.LuckySpikeCandidateTradeMaxPages,
			CandidateMinSampleTrades: cfg.LuckySpikeCandidateMinSampleTrades,
			MaxCandidateWallets:      cfg.LuckySpikeMaxCandidateWallets,
			WalletConcurrency:        cfg.LuckySpikeWalletConcurrency,
			WalletTradePageSize:      cfg.LuckySpikeWalletTradePageSize,
			WalletTradeMaxPages:      cfg.LuckySpikeWalletTradeMaxPages,
			WalletActivityMaxPages:   cfg.LuckySpikeWalletActivityMaxPages,
			PerWalletFetchTimout:     cfg.LuckySpikePerWalletTimeout,
			Params: walletintel.LuckySpikeParams{
				Lookback:            cfg.LuckySpikeLookback,
				MaxAvgTradeInterval: cfg.LuckySpikeMaxAvgTradeInterval,
				MinProfitPct:        cfg.LuckySpikeMinProfitPct,
				MinTradesPerWeek:    cfg.LuckySpikeMinTradesPerWeek,
				MinTradesPerMonth:   cfg.LuckySpikeMinTradesPerMonth,
				MinCoverage:         cfg.LuckySpikeMinCoverage,
				MinObservedTrades:   cfg.LuckySpikeMinObservedTrades,
				MinObservedCoverage: cfg.LuckySpikeMinObservedCoverage,
				MinEntryNotional:    cfg.LuckySpikeMinEntryNotional,
				MinRealizedPnL:      cfg.LuckySpikeMinRealizedPnL,
				MinRealizedCycles:   cfg.LuckySpikeMinRealizedCycles,
				MinScore:            cfg.LuckySpikeMinScore,
				MinConfidence:       cfg.LuckySpikeMinConfidence,
			},
		})
	}
	if cfg.MLBLateGameEnabled {
		a.Workers = append(a.Workers, &marketscan.MLBLateGameWorker{
			MLB:            mlbCli,
			Store:          store,
			Router:         router,
			Log:            log,
			Links:          links,
			Enabled:        true,
			Interval:       cfg.MLBLateGameInterval,
			MinInning:      cfg.MLBLateGameMinInning,
			MinAwayDeficit: cfg.MLBLateGameMinAwayDeficit,
			MarketLimit:    cfg.MLBLateGameMarketLimit,
		})
	}
	if cfg.RetentionEnabled {
		a.Workers = append(a.Workers, &retention.Worker{
			Store:                        store,
			Log:                          log,
			Enabled:                      true,
			Interval:                     cfg.RetentionInterval,
			PerTableTimeout:              cfg.RetentionPerTableTimeout,
			BatchSize:                    cfg.RetentionBatchSize,
			WalletClosedPositionsMaxRows: cfg.RetentionWalletClosedPositionsMaxRows,
			MarketPriceSamplesMaxRows:    cfg.RetentionMarketPriceSamplesMaxRows,
			HolderSnapshotsMaxRows:       cfg.RetentionHolderSnapshotsMaxRows,
			CandidateEvidenceMaxRows:     cfg.RetentionCandidateEvidenceMaxRows,
			WalletScoresMaxRows:          cfg.RetentionWalletScoresMaxRows,
		})
	}
	return a, nil
}

// Run starts all workers and the metrics HTTP server. Blocks until ctx
// is cancelled, then waits for workers to drain.
func (a *App) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(a.Workers)+2)

	// CLOB WS
	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- a.CLOBWS.Run(ctx)
	}()

	// Metrics + health/ready
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Default.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		rctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.Store.Pool.Ping(rctx); err != nil {
			http.Error(w, "db ping failed: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		var have int
		if err := a.Store.Pool.QueryRow(rctx, `SELECT count(*) FROM information_schema.tables WHERE table_name IN ('events','markets','wallets','alert_decisions','telegram_deliveries','market_price_samples')`).Scan(&have); err != nil {
			http.Error(w, "schema check failed: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if have < 6 {
			http.Error(w, "migrations incomplete", http.StatusServiceUnavailable)
			return
		}
		if a.Cfg.TelegramBotToken == "" {
			http.Error(w, "telegram not configured", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})
	srv := &http.Server{
		Addr:              a.Cfg.MetricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Workers
	for _, w := range a.Workers {
		wg.Add(1)
		go func(worker Worker) {
			defer wg.Done()
			if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errCh <- err
			}
		}(w)
	}
	a.Log.Info("workers started", "count", len(a.Workers))

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil && !errors.Is(e, context.Canceled) {
			return e
		}
	}
	return nil
}

func (a *App) Close() {
	if a.Store != nil {
		a.Store.Close()
	}
}
