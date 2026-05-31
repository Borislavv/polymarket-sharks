package config

import (
	"strings"
	"testing"
)

func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func baseEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":              "postgres://x",
		"TELEGRAM_BOT_TOKEN":        "tok",
		"TELEGRAM_ADMIN_CHAT_ID":    "1",
		"TELEGRAM_BETS_CHAT_ID":     "2",
		"TELEGRAM_CLUSTERS_CHAT_ID": "3",
		"TELEGRAM_NEWS_CHAT_ID":     "4",
	}
}

func TestLoad_MissingTelegramTokenFails(t *testing.T) {
	env := baseEnv()
	delete(env, "TELEGRAM_BOT_TOKEN")
	_, err := LoadFromEnv(fakeEnv(env))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("expected TELEGRAM_BOT_TOKEN missing error, got %v", err)
	}
}

func TestLoad_MissingDatabaseURLFails(t *testing.T) {
	env := baseEnv()
	delete(env, "DATABASE_URL")
	_, err := LoadFromEnv(fakeEnv(env))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected DATABASE_URL missing error, got %v", err)
	}
}

func TestLoad_DefaultsLoaded(t *testing.T) {
	c, err := LoadFromEnv(fakeEnv(baseEnv()))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.SharkMinTrades != 100 {
		t.Fatalf("default SharkMinTrades expected 100, got %d", c.SharkMinTrades)
	}
	if c.InsiderMinNotionalUSD != 20000 {
		t.Fatalf("default InsiderMinNotionalUSD expected 20000, got %f", c.InsiderMinNotionalUSD)
	}
	if c.ClusterWindowBefore.Hours() != 3 {
		t.Fatalf("default ClusterWindowBefore expected 3h, got %v", c.ClusterWindowBefore)
	}
	if c.ClusterMinWallets != 2 {
		t.Fatalf("default ClusterMinWallets expected 2, got %d", c.ClusterMinWallets)
	}
	if !contains(c.TargetCategories, "politics") {
		t.Fatalf("expected politics category in defaults, got %v", c.TargetCategories)
	}
}

func TestLoad_InvalidThresholdFails(t *testing.T) {
	env := baseEnv()
	env["SHARK_MIN_SCORE"] = "9999"
	_, err := LoadFromEnv(fakeEnv(env))
	if err == nil || !strings.Contains(err.Error(), "SHARK_MIN_SCORE") {
		t.Fatalf("expected SHARK_MIN_SCORE range error, got %v", err)
	}
}

func TestLoad_InvalidConfidenceFails(t *testing.T) {
	env := baseEnv()
	env["INSIDER_MIN_CONFIDENCE"] = "1.5"
	_, err := LoadFromEnv(fakeEnv(env))
	if err == nil {
		t.Fatalf("expected confidence range error")
	}
}

func TestLoad_InvalidDurationFails(t *testing.T) {
	env := baseEnv()
	env["DISCOVERY_INTERVAL"] = "not-a-duration"
	_, err := LoadFromEnv(fakeEnv(env))
	if err == nil {
		t.Fatalf("expected duration parse error")
	}
}

func TestLoad_AlertingEnabledDefault(t *testing.T) {
	c, err := LoadFromEnv(fakeEnv(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AlertingEnabled {
		t.Fatalf("default AlertingEnabled should be true")
	}
	if !c.LifecycleEnabled {
		t.Fatalf("default LifecycleEnabled should be true")
	}
	if !c.ExitAlertsEnabled {
		t.Fatalf("default ExitAlertsEnabled should be true")
	}
	if c.ExitClusterEnabled {
		t.Fatalf("default ExitClusterEnabled should be false")
	}
}

func TestLoad_AlertingDisabled(t *testing.T) {
	env := baseEnv()
	env["ALERTING_ENABLED"] = "false"
	c, err := LoadFromEnv(fakeEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if c.AlertingEnabled {
		t.Fatalf("ALERTING_ENABLED=false must disable")
	}
}

func TestLoad_HotsetLimit_DefaultIs400(t *testing.T) {
	c, err := LoadFromEnv(fakeEnv(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.HotsetMaxMarkets != 400 {
		t.Fatalf("default HotsetMaxMarkets expected 400, got %d", c.HotsetMaxMarkets)
	}
}

func TestLoad_HotsetLimit_CanBeSetTo80(t *testing.T) {
	env := baseEnv()
	env["HOTSET_MARKETS_LIMIT"] = "80"
	c, err := LoadFromEnv(fakeEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if c.HotsetMaxMarkets != 80 {
		t.Fatalf("HotsetMaxMarkets expected 80, got %d", c.HotsetMaxMarkets)
	}
}

func TestLoad_HotsetLegacyAlias_HotsetMaxMarkets(t *testing.T) {
	env := baseEnv()
	env["HOTSET_MAX_MARKETS"] = "250"
	c, err := LoadFromEnv(fakeEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if c.HotsetMaxMarkets != 250 {
		t.Fatalf("legacy HOTSET_MAX_MARKETS=250 expected 250, got %d", c.HotsetMaxMarkets)
	}
}

func TestLoad_HotsetMarketsLimit_TakesPrecedenceOverLegacy(t *testing.T) {
	env := baseEnv()
	env["HOTSET_MARKETS_LIMIT"] = "400"
	env["HOTSET_MAX_MARKETS"] = "80"
	c, err := LoadFromEnv(fakeEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	// HOTSET_MARKETS_LIMIT takes precedence
	if c.HotsetMaxMarkets != 400 {
		t.Fatalf("HOTSET_MARKETS_LIMIT should take precedence: want 400, got %d", c.HotsetMaxMarkets)
	}
}

func TestLoad_ProfitGate_DefaultsEnabled(t *testing.T) {
	c, err := LoadFromEnv(fakeEnv(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AlertProfitGateEnabled {
		t.Fatal("default AlertProfitGateEnabled should be true")
	}
}

func TestLoad_ProfitGate_DefaultTierThresholds(t *testing.T) {
	c, err := LoadFromEnv(fakeEnv(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.AlertTinyMaxNotionalUSD != 500 {
		t.Fatalf("AlertTinyMaxNotionalUSD default 500, got %g", c.AlertTinyMaxNotionalUSD)
	}
	if c.AlertTinyMinOdds != 10 {
		t.Fatalf("AlertTinyMinOdds default 10, got %g", c.AlertTinyMinOdds)
	}
	if c.AlertTinyMinProfitUSD != 4000 {
		t.Fatalf("AlertTinyMinProfitUSD default 4000, got %g", c.AlertTinyMinProfitUSD)
	}
	if c.AlertSmallMaxNotionalUSD != 2000 {
		t.Fatalf("AlertSmallMaxNotionalUSD default 2000, got %g", c.AlertSmallMaxNotionalUSD)
	}
	if c.AlertSmallMinOdds != 7 {
		t.Fatalf("AlertSmallMinOdds default 7, got %g", c.AlertSmallMinOdds)
	}
	if c.AlertSmallMinProfitUSD != 7000 {
		t.Fatalf("AlertSmallMinProfitUSD default 7000, got %g", c.AlertSmallMinProfitUSD)
	}
	if c.AlertMediumMaxNotionalUSD != 10000 {
		t.Fatalf("AlertMediumMaxNotionalUSD default 10000, got %g", c.AlertMediumMaxNotionalUSD)
	}
	if c.AlertMediumMinOdds != 4 {
		t.Fatalf("AlertMediumMinOdds default 4, got %g", c.AlertMediumMinOdds)
	}
	if c.AlertMediumMinProfitUSD != 15000 {
		t.Fatalf("AlertMediumMinProfitUSD default 15000, got %g", c.AlertMediumMinProfitUSD)
	}
	if c.AlertLargeMaxNotionalUSD != 80000 {
		t.Fatalf("AlertLargeMaxNotionalUSD default 80000, got %g", c.AlertLargeMaxNotionalUSD)
	}
	if c.AlertLargeMinOdds != 2 {
		t.Fatalf("AlertLargeMinOdds default 2, got %g", c.AlertLargeMinOdds)
	}
	if c.AlertLargeMinProfitUSD != 25000 {
		t.Fatalf("AlertLargeMinProfitUSD default 25000, got %g", c.AlertLargeMinProfitUSD)
	}
	if c.AlertMegaMinNotionalUSD != 80000 {
		t.Fatalf("AlertMegaMinNotionalUSD default 80000, got %g", c.AlertMegaMinNotionalUSD)
	}
	if c.AlertMegaMinOdds != 1.15 {
		t.Fatalf("AlertMegaMinOdds default 1.15, got %g", c.AlertMegaMinOdds)
	}
	if c.AlertMegaMinProfitUSD != 10000 {
		t.Fatalf("AlertMegaMinProfitUSD default 10000, got %g", c.AlertMegaMinProfitUSD)
	}
}

func TestLoad_ProfitGate_CustomValues(t *testing.T) {
	env := baseEnv()
	env["ALERT_PROFIT_GATE_ENABLED"] = "false"
	env["ALERT_TINY_MAX_NOTIONAL_USD"] = "300"
	env["ALERT_MEGA_MIN_ODDS"] = "1.5"
	env["ALERT_MEGA_MIN_PROFIT_USD"] = "20000"
	c, err := LoadFromEnv(fakeEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if c.AlertProfitGateEnabled {
		t.Fatal("ALERT_PROFIT_GATE_ENABLED=false should disable gate")
	}
	if c.AlertTinyMaxNotionalUSD != 300 {
		t.Fatalf("AlertTinyMaxNotionalUSD: want 300, got %g", c.AlertTinyMaxNotionalUSD)
	}
	if c.AlertMegaMinOdds != 1.5 {
		t.Fatalf("AlertMegaMinOdds: want 1.5, got %g", c.AlertMegaMinOdds)
	}
	if c.AlertMegaMinProfitUSD != 20000 {
		t.Fatalf("AlertMegaMinProfitUSD: want 20000, got %g", c.AlertMegaMinProfitUSD)
	}
}

func TestLoad_ClusterProfitGate_Defaults(t *testing.T) {
	c, err := LoadFromEnv(fakeEnv(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.ClusterMinTotalProfitUSD != 25000 {
		t.Fatalf("ClusterMinTotalProfitUSD default 25000, got %g", c.ClusterMinTotalProfitUSD)
	}
	if c.ClusterMinAvgOdds != 2.0 {
		t.Fatalf("ClusterMinAvgOdds default 2.0, got %g", c.ClusterMinAvgOdds)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}
