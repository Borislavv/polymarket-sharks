package alerts

import (
	"fmt"
	"strings"

	"github.com/Borislavv/polymarket-sharks/internal/telegram"
)

// mdLink is the legacy in-package helper kept for the formatters that still
// need to render a label-with-optional-URL inline. It now defers to the
// centralized link rendering in LinkBuilder so URL escaping/validation is
// consistent. When url is empty the helper drops the link entirely (rather
// than emitting a plain "Trader" that pretends to be a link).
func mdLink(label, url string) string {
	if url == "" {
		return telegram.EscapeMD(label)
	}
	return mdLinkEscaped(label, url)
}

func usd(v float64) string {
	abs := v
	if abs < 0 {
		abs = -abs
	}
	if abs >= 1_000_000 {
		return fmt.Sprintf("$%.2fM", v/1_000_000)
	}
	if abs >= 1_000 {
		return fmt.Sprintf("$%.1fk", v/1_000)
	}
	if abs >= 100 {
		return fmt.Sprintf("$%.0f", v)
	}
	if abs >= 1 {
		return fmt.Sprintf("$%.2f", v)
	}
	// sub-dollar: never render as $0 when v > 0
	if abs > 0 {
		return fmt.Sprintf("$%.2f", v)
	}
	return "$0"
}

func priceCents(p float64) string {
	return fmt.Sprintf("%.0f¢", p*100)
}

func ratioX(o float64) string {
	if o <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("x%.2f", o)
}

func pctStr(v float64) string {
	return fmt.Sprintf("%.0f%%", v*100)
}

// usdOrNA renders dollars when `known` is true, else "n/a". We never display
// fake "$0" for missing data because the alert is an evidence document, not
// a placeholder template.
func usdOrNA(v float64, known bool) string {
	if !known {
		return "n/a"
	}
	return usd(v)
}

// signedUsdOrNA shows -$X for negative losses (the avg/median loss columns
// are stored as negative numbers), "n/a" when unknown.
func signedUsdOrNA(v float64, known bool) string {
	if !known {
		return "n/a"
	}
	if v == 0 {
		return "$0"
	}
	if v < 0 {
		return "-" + usd(-v)
	}
	return usd(v)
}

func pctOrNA(v float64) string {
	if v < 0 {
		return "n/a"
	}
	return pctStr(v)
}

func floatOrNA(v float64, fmtStr string) string {
	if v <= 0 {
		return "n/a"
	}
	return fmt.Sprintf(fmtStr, v)
}

func intOrNA(v int) string {
	if v == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d", v)
}

// orZero returns the first non-zero value, or 0 when none match.
func orZero(vs ...float64) float64 {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

func orZeroInt(vs ...int) int {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

func nonZero(vs ...float64) bool {
	for _, v := range vs {
		if v != 0 {
			return true
		}
	}
	return false
}

// FormatSharkBet renders a SHARK IS MOVING message. v4 includes the
// historical closed-position evidence (closed count, ROI, win-rate, avg
// stake, realized PnL) used to qualify the wallet as a shark.
func FormatSharkBet(a SharkBet, links LinkBuilder) string {
	var b strings.Builder
	grade := scoreGrade(a.Score)
	fmt.Fprintf(&b, "*SHARK IS MOVING* · *WARNING*\n\n")
	traderLink := links.TraderLink(displayName(a.Pseudonym, a.WalletShort), a.ProfileSlug, a.WalletFull)
	if traderLink == "" {
		fmt.Fprintf(&b, "Trader: %s\n", telegram.EscapeMD(displayName(a.Pseudonym, a.WalletShort)))
	} else {
		fmt.Fprintf(&b, "Trader: %s\n", traderLink)
	}
	fmt.Fprintf(&b, "Class: %s\n", telegram.EscapeMD(a.Class))
	if a.PromotionPath != "" {
		fmt.Fprintf(&b, "Path: %s\n", telegram.EscapeMD(strings.ReplaceAll(a.PromotionPath, "_", " ")))
	}
	fmt.Fprintf(&b, "Quality: %s · score %s/100 · confidence %s\n",
		telegram.EscapeMD(grade),
		telegram.EscapeMD(fmt.Sprintf("%d", a.Score)),
		telegram.EscapeMD(fmt.Sprintf("%.2f", a.Confidence)))
	histPnL := "n/a"
	if a.HistoricalPnLKnown {
		histPnL = usd(a.HistoricalPnL)
	}
	wr := a.HistoricalWinRate
	if wr == 0 {
		wr = a.WinRate
	}
	if a.RealizedCycles > 0 {
		fmt.Fprintf(&b, "Realized: %s cycles · profitable exit rate %s · ROI %s\n",
			telegram.EscapeMD(fmt.Sprintf("%d", a.RealizedCycles)),
			telegram.EscapeMD(pctStr(a.ProfitableExitRate)),
			telegram.EscapeMD(pctStr(a.RealizedAvgROI)))
		fmt.Fprintf(&b, "Avg realized trade: %s · Realized PnL: %s\n",
			telegram.EscapeMD(usd(a.RealizedAvgNotional)),
			telegram.EscapeMD(usd(a.RealizedTotalPnL)))
	} else {
		// API-closed-positions fallback (still trading PnL, not outcome).
		fmt.Fprintf(&b, "Closed: %s positions · profitable exit rate %s · ROI %s\n",
			telegram.EscapeMD(fmt.Sprintf("%d", a.ClosedPositionsCount)),
			telegram.EscapeMD(pctStr(wr)),
			telegram.EscapeMD(pctStr(a.HistoricalROI)))
		fmt.Fprintf(&b, "Avg realized trade: %s · Realized PnL: %s\n",
			telegram.EscapeMD(usd(a.AvgClosedStake)),
			telegram.EscapeMD(histPnL))
	}
	if a.ScoringBasis != "" {
		fmt.Fprintf(&b, "Scoring basis: %s\n",
			telegram.EscapeMD(strings.ReplaceAll(a.ScoringBasis, "_", " ")))
	}
	dirLabel := string(a.Direction)
	if a.Outcome != "" && Direction(a.Direction).isCategorical() {
		dirLabel = string(a.Direction) + " (" + a.Outcome + ")"
	}
	fmt.Fprintf(&b, "New bet: %s on %s\n",
		telegram.EscapeMD(dirLabel),
		telegram.EscapeMD(a.MarketTitle))
	fmt.Fprintf(&b, "Size: %s @ %s\n",
		telegram.EscapeMD(usd(a.Notional)),
		telegram.EscapeMD(priceCents(a.Price)))
	fmt.Fprintf(&b, "Odds: %s · Payoff if win: %s\n",
		telegram.EscapeMD(ratioX(a.Odds)),
		telegram.EscapeMD(usd(a.Payoff)))
	if a.ProfitIfWin > 0 {
		fmt.Fprintf(&b, "Profit if win: %s\n",
			telegram.EscapeMD(usd(a.ProfitIfWin)))
	}
	if a.AlertGateTier != "" {
		fmt.Fprintf(&b, "Tier: %s\n", telegram.EscapeMD(a.AlertGateTier))
	}
	if a.AlertGateReason != "" {
		fmt.Fprintf(&b, "Why alert: %s\n", telegram.EscapeMD(a.AlertGateReason))
	} else if a.ReasonHumanized != "" {
		fmt.Fprintf(&b, "Why: %s\n", telegram.EscapeMD(a.ReasonHumanized))
	}
	emitLinks(&b, JoinLinks(
		links.MarketLink("Market", a.MarketSlug),
		links.EventLink("Event", a.EventSlug),
		links.TraderLink("Trader", a.ProfileSlug, a.WalletFull),
	))
	return b.String()
}

// emitLinks appends the canonical "Links: …" footer to the message body
// only when at least one clickable link exists. Empty input is silently
// dropped so we never emit "Links: Trader" without a URL.
func emitLinks(b *strings.Builder, joined string) {
	if joined == "" {
		return
	}
	fmt.Fprintf(b, "Links: %s", joined)
}

// FormatInsiderBet renders the INSIDER-LIKE alert. v4 distinguishes:
//   - first big bet              → "FIRST BIG BET" title
//   - winning streak continues   → "STREAK CONTINUES"
//   - streak just broke          → "STREAK BROKEN" (admin/info)
//
// Always includes the "suspicious informed-flow candidate, not a legal
// insider claim" disclaimer to honour the NON_LEGAL claim guarantee.
func FormatInsiderBet(a InsiderBet, links LinkBuilder) string {
	var b strings.Builder
	sev := a.Severity
	if sev == "" {
		sev = "HIGH"
	}
	title := "*INSIDER\\-LIKE FIRST BIG BET*"
	switch a.StreakState {
	case "continues":
		title = "*INSIDER\\-LIKE STREAK CONTINUES*"
	case "broken":
		title = "*INSIDER\\-LIKE STREAK BROKEN*"
	}
	fmt.Fprintf(&b, "%s · *%s*\n\n", title, telegram.EscapeMD(sev))
	walletLink := links.TraderLink(a.WalletShort, a.ProfileSlug, a.WalletFull)
	if walletLink == "" {
		fmt.Fprintf(&b, "Wallet: %s\n", telegram.EscapeMD(a.WalletShort))
	} else {
		fmt.Fprintf(&b, "Wallet: %s\n", walletLink)
	}
	fmt.Fprintf(&b, "Class: %s\n", telegram.EscapeMD("insider-like candidate (unusual conviction)"))
	fmt.Fprintf(&b, "Lifetime: %s trades · %s markets\n",
		telegram.EscapeMD(fmt.Sprintf("%d", a.LifetimeTrades)),
		telegram.EscapeMD(fmt.Sprintf("%d", a.LifetimeMarkets)))
	if a.FirstTradeAt != "" {
		fmt.Fprintf(&b, "First trade: %s\n", telegram.EscapeMD(a.FirstTradeAt))
	}
	fmt.Fprintf(&b, "Streak: %s · wins %s · losses %s\n",
		telegram.EscapeMD(streakLabel(a.StreakState)),
		telegram.EscapeMD(fmt.Sprintf("%d", a.ClosedWins)),
		telegram.EscapeMD(fmt.Sprintf("%d", a.ClosedLosses)))
	insiderDirLabel := string(a.Direction)
	if a.DirectionOutcome != "" {
		insiderDirLabel = string(a.Direction) + " (" + a.DirectionOutcome + ")"
	}
	fmt.Fprintf(&b, "Move: %s on %s\n",
		telegram.EscapeMD(insiderDirLabel),
		telegram.EscapeMD(a.MarketTitle))
	fmt.Fprintf(&b, "Size: %s @ %s\n",
		telegram.EscapeMD(usd(a.Notional)),
		telegram.EscapeMD(priceCents(a.Price)))
	fmt.Fprintf(&b, "Odds: %s · Payoff if win: %s\n",
		telegram.EscapeMD(ratioX(a.Odds)),
		telegram.EscapeMD(usd(a.Payoff)))
	if a.ProfitIfWin > 0 {
		fmt.Fprintf(&b, "Profit if win: %s\n",
			telegram.EscapeMD(usd(a.ProfitIfWin)))
	}
	if a.ReasonHumanized != "" {
		fmt.Fprintf(&b, "Why: %s\n", telegram.EscapeMD(a.ReasonHumanized))
	}
	fmt.Fprintf(&b, "Note: %s\n",
		telegram.EscapeMD("suspicious informed-flow candidate, not a legal insider claim"))
	emitLinks(&b, JoinLinks(
		links.MarketLink("Market", a.MarketSlug),
		links.EventLink("Event", a.EventSlug),
		links.TraderLink("Trader", a.ProfileSlug, a.WalletFull),
	))
	return b.String()
}

// FormatSharkDiscovered renders the admin-only SHARK_DISCOVERED message.
func FormatSharkDiscovered(a DiscoveryAlert, links LinkBuilder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*SHARK DISCOVERED* · *ADMIN*\n\n")
	traderLink := links.TraderLink(displayName(a.Pseudonym, a.WalletShort), a.ProfileSlug, a.WalletFull)
	if traderLink == "" {
		fmt.Fprintf(&b, "Trader: %s\n", telegram.EscapeMD(displayName(a.Pseudonym, a.WalletShort)))
	} else {
		fmt.Fprintf(&b, "Trader: %s\n", traderLink)
	}
	fmt.Fprintf(&b, "Class: %s\n", telegram.EscapeMD("shark"))
	fmt.Fprintf(&b, "Status: %s\n", telegram.EscapeMD(a.Status))
	if a.PromotionPath != "" {
		fmt.Fprintf(&b, "Promotion path: %s\n", telegram.EscapeMD(strings.ReplaceAll(a.PromotionPath, "_", " ")))
	}
	fmt.Fprintf(&b, "Score: %s/100 · confidence %s\n",
		telegram.EscapeMD(fmt.Sprintf("%d", a.Score)),
		telegram.EscapeMD(fmt.Sprintf("%.2f", a.Confidence)))
	if a.ReasonHumanized != "" {
		fmt.Fprintf(&b, "\nWhy selected:\n· %s\n", telegram.EscapeMD(a.ReasonHumanized))
	}
	// ----- Performance section -----
	realizedTotal := a.ProfitableCount + a.LosingCount + a.BreakevenCount
	if realizedTotal == 0 && a.RealizedCycles > 0 {
		realizedTotal = a.RealizedCycles
	}
	if realizedTotal == 0 && a.ClosedPositions > 0 {
		realizedTotal = a.ClosedPositions
	}
	successRatePct := -1.0
	if realizedTotal > 0 {
		successRatePct = float64(a.ProfitableCount) / float64(realizedTotal)
	} else if a.ProfitableExitRate > 0 {
		successRatePct = a.ProfitableExitRate
	} else if a.WinRate > 0 {
		successRatePct = a.WinRate
	}

	fmt.Fprintf(&b, "\nPerformance:\n")
	if realizedTotal > 0 || successRatePct >= 0 {
		fmt.Fprintf(&b, "· profitable exits: %s/%s \\(%s\\)\n",
			telegram.EscapeMD(fmt.Sprintf("%d", a.ProfitableCount)),
			telegram.EscapeMD(fmt.Sprintf("%d", realizedTotal)),
			telegram.EscapeMD(pctOrNA(successRatePct)))
	} else {
		fmt.Fprintf(&b, "· profitable exits: %s\n", telegram.EscapeMD("n/a"))
	}
	pnlValue := orZero(a.RealizedTotalPnL, a.RealizedPnL)
	pnlKnown := a.RealizedTotalPnL != 0 || a.HistoricalPnLKnown
	fmt.Fprintf(&b, "· realized PnL: %s\n", telegram.EscapeMD(usdOrNA(pnlValue, pnlKnown)))
	fmt.Fprintf(&b, "· realized ROI: %s\n", telegram.EscapeMD(pctOrNA(orZero(a.RealizedAvgROI, a.ROI))))
	fmt.Fprintf(&b, "· profit factor: %s\n", telegram.EscapeMD(floatOrNA(a.RealizedProfitFactor, "%.2f")))
	fmt.Fprintf(&b, "· avg trade: %s\n", telegram.EscapeMD(usdOrNA(orZero(a.AvgTradeNotional, a.RealizedAvgNotional, a.AvgClosedStake),
		nonZero(a.AvgTradeNotional, a.RealizedAvgNotional, a.AvgClosedStake))))
	fmt.Fprintf(&b, "· median trade: %s\n", telegram.EscapeMD(usdOrNA(a.MedianTradeNotional, a.MedianTradeNotional > 0)))
	fmt.Fprintf(&b, "· avg win: %s\n", telegram.EscapeMD(usdOrNA(a.AvgWinUSD, a.AvgWinUSD > 0)))
	fmt.Fprintf(&b, "· avg loss: %s\n", telegram.EscapeMD(signedUsdOrNA(a.AvgLossUSD, a.AvgLossUSD < 0)))
	fmt.Fprintf(&b, "· max win/loss: %s / %s\n",
		telegram.EscapeMD(usdOrNA(a.MaxWinUSD, a.MaxWinUSD > 0)),
		telegram.EscapeMD(signedUsdOrNA(a.MaxLossUSD, a.MaxLossUSD < 0)))

	// ----- Sample section -----
	fmt.Fprintf(&b, "\nSample:\n")
	period := "n/a"
	if a.EvaluatedFrom != "" && a.EvaluatedTo != "" {
		period = fmt.Sprintf("%s → %s (%dd)", a.EvaluatedFrom, a.EvaluatedTo, a.HistoryDays)
	}
	fmt.Fprintf(&b, "· evaluated period: %s\n", telegram.EscapeMD(period))
	fmt.Fprintf(&b, "· trades checked: %s\n", telegram.EscapeMD(intOrNA(a.TradesChecked)))
	fmt.Fprintf(&b, "· positions checked: %s\n", telegram.EscapeMD(intOrNA(a.PositionsChecked)))
	fmt.Fprintf(&b, "· realized cycles checked: %s\n", telegram.EscapeMD(intOrNA(orZeroInt(a.RealizedCyclesCheck, a.RealizedCycles, realizedTotal))))
	fmt.Fprintf(&b, "· open/unresolved excluded: %s\n", telegram.EscapeMD(intOrNA(a.OpenUnresolvedCount)))
	basis := a.ScoringBasis
	if basis == "" {
		basis = "api_closed_positions"
	}
	fmt.Fprintf(&b, "· scoring basis: %s\n", telegram.EscapeMD(strings.ReplaceAll(basis, "_", " ")))
	if a.DataQuality != "" {
		fmt.Fprintf(&b, "· data quality: %s\n", telegram.EscapeMD(a.DataQuality))
	} else {
		fmt.Fprintf(&b, "· data quality: %s\n", telegram.EscapeMD("n/a"))
	}
	fmt.Fprintf(&b, "\nWhat happens next:\n· wallet added to watchlist\n· future qualifying bets will trigger SHARK alerts\n\n")
	dashLink := ""
	if a.DashboardURL != "" {
		dashLink = mdLinkEscaped("Dashboard", a.DashboardURL)
	}
	emitLinks(&b, JoinLinks(
		links.TraderLink("Trader", a.ProfileSlug, a.WalletFull),
		links.MarketLink("Market", a.MarketSlug),
		links.EventLink("Event", a.EventSlug),
		dashLink,
	))
	return b.String()
}

// FormatInsiderLikeDiscovered renders the admin-only INSIDER_LIKE_DISCOVERED message.
func FormatInsiderLikeDiscovered(a DiscoveryAlert, links LinkBuilder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*INSIDER\\-LIKE DISCOVERED* · *ADMIN*\n\n")
	walletLink := links.TraderLink(a.WalletShort, a.ProfileSlug, a.WalletFull)
	if walletLink == "" {
		fmt.Fprintf(&b, "Wallet: %s\n", telegram.EscapeMD(a.WalletShort))
	} else {
		fmt.Fprintf(&b, "Wallet: %s\n", walletLink)
	}
	fmt.Fprintf(&b, "Class: %s\n", telegram.EscapeMD("insider-like candidate"))
	fmt.Fprintf(&b, "Status: %s\n", telegram.EscapeMD(a.Status))
	trigger := "first big bet"
	switch a.StreakState {
	case "continues":
		trigger = "clean streak continues"
	case "broken":
		trigger = "streak just broken"
	case "clean":
		trigger = "first big bet (clean history)"
	}
	fmt.Fprintf(&b, "Trigger: %s\n", telegram.EscapeMD(trigger))
	fmt.Fprintf(&b, "Score: %s/100 · confidence %s\n",
		telegram.EscapeMD(fmt.Sprintf("%d", a.Score)),
		telegram.EscapeMD(fmt.Sprintf("%.2f", a.Confidence)))
	if a.ReasonHumanized != "" {
		fmt.Fprintf(&b, "\nWhy selected:\n· %s\n", telegram.EscapeMD(a.ReasonHumanized))
	}
	fmt.Fprintf(&b, "\nStats:\n")
	fmt.Fprintf(&b, "· lifetime trades: %s\n", telegram.EscapeMD(fmt.Sprintf("%d", a.LifetimeTrades)))
	fmt.Fprintf(&b, "· wins/losses: %s/%s\n",
		telegram.EscapeMD(fmt.Sprintf("%d", a.ClosedWins)),
		telegram.EscapeMD(fmt.Sprintf("%d", a.ClosedLosses)))
	if a.LatestBetUSD > 0 {
		fmt.Fprintf(&b, "· latest bet: %s @ %s\n",
			telegram.EscapeMD(usd(a.LatestBetUSD)),
			telegram.EscapeMD(ratioX(a.LatestBetOdds)))
	}
	if a.FirstTradeAt != "" {
		fmt.Fprintf(&b, "· first trade: %s\n", telegram.EscapeMD(a.FirstTradeAt))
	}
	if a.DataQuality != "" {
		fmt.Fprintf(&b, "· data quality: %s\n", telegram.EscapeMD(a.DataQuality))
	}
	fmt.Fprintf(&b, "\nNote: %s\n",
		telegram.EscapeMD("suspicious informed-flow candidate, not a legal insider claim"))
	fmt.Fprintf(&b, "\nWhat happens next:\n· wallet added to insider watchlist\n· next large/high\\-odds move will trigger streak alert\n\n")
	dashLink := ""
	if a.DashboardURL != "" {
		dashLink = mdLinkEscaped("Dashboard", a.DashboardURL)
	}
	emitLinks(&b, JoinLinks(
		links.TraderLink("Trader", a.ProfileSlug, a.WalletFull),
		links.MarketLink("Market", a.MarketSlug),
		links.EventLink("Event", a.EventSlug),
		dashLink,
	))
	return b.String()
}

// FormatLuckySpikeCandidate renders an admin-only alert for wallets that
// match the lucky-spike strategy (high frequency + elevated profit percentage).
func FormatLuckySpikeCandidate(a LuckySpikeAlert, links LinkBuilder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*LUCKY SPIKE CANDIDATE* · *ADMIN*\n\n")

	traderLink := links.TraderLink(displayName(a.Pseudonym, a.WalletShort), a.ProfileSlug, a.WalletFull)
	if traderLink == "" {
		fmt.Fprintf(&b, "Trader: %s\n", telegram.EscapeMD(displayName(a.Pseudonym, a.WalletShort)))
	} else {
		fmt.Fprintf(&b, "Trader: %s\n", traderLink)
	}
	fmt.Fprintf(&b, "Class: %s\n", telegram.EscapeMD("insider-like candidate (weekly luck spike)"))
	fmt.Fprintf(&b, "Score: %s/100 · confidence %s\n",
		telegram.EscapeMD(fmt.Sprintf("%d", a.Score)),
		telegram.EscapeMD(fmt.Sprintf("%.2f", a.Confidence)))

	if a.ReasonHumanized != "" {
		fmt.Fprintf(&b, "\nWhy selected:\n· %s\n", telegram.EscapeMD(a.ReasonHumanized))
	}

	fmt.Fprintf(&b, "\nWeekly stats:\n")
	fmt.Fprintf(&b, "· trades: %s across %s markets\n",
		telegram.EscapeMD(fmt.Sprintf("%d", a.WeeklyTradeCount)),
		telegram.EscapeMD(fmt.Sprintf("%d", a.WeeklyDistinctMarkets)))
	fmt.Fprintf(&b, "· profitable exits: %s/%s\n",
		telegram.EscapeMD(fmt.Sprintf("%d", a.WeeklyProfitableCycles)),
		telegram.EscapeMD(fmt.Sprintf("%d", a.WeeklyRealizedCycles)),
	)
	fmt.Fprintf(&b, "· weekly profit %%: %s\n",
		telegram.EscapeMD(pctOrNA(a.WeeklyProfitPct)))
	fmt.Fprintf(&b, "· monthly profit %%: %s\n",
		telegram.EscapeMD(pctOrNA(a.MonthlyProfitPct)))
	fmt.Fprintf(&b, "· avg trade interval: %s min\n",
		telegram.EscapeMD(fmt.Sprintf("%.2f", a.AvgTradeIntervalMinutes)))
	fmt.Fprintf(&b, "· monthly avg interval: %s min\n",
		telegram.EscapeMD(fmt.Sprintf("%.2f", a.MonthlyAvgTradeIntervalMinutes)))
	if a.MonthlyTradeCount > 0 || a.MonthlyRealizedCycles > 0 {
		fmt.Fprintf(&b, "· monthly trades/cycles: %s/%s\n",
			telegram.EscapeMD(fmt.Sprintf("%d", a.MonthlyTradeCount)),
			telegram.EscapeMD(fmt.Sprintf("%d", a.MonthlyRealizedCycles)))
	}
	fmt.Fprintf(&b, "· activity coverage: %s h\n",
		telegram.EscapeMD(fmt.Sprintf("%.1f", a.WeeklyCoverageHours)))
	if a.AvgWeeklyTradeNotionalUSD > 0 {
		fmt.Fprintf(&b, "· avg trade notional: %s\n",
			telegram.EscapeMD(usd(a.AvgWeeklyTradeNotionalUSD)))
	}
	if a.DataQuality != "" {
		fmt.Fprintf(&b, "· data quality: %s\n",
			telegram.EscapeMD(a.DataQuality))
	}
	if a.TradeHistoryPartialHint != "" {
		fmt.Fprintf(&b, "· partial-history hint: %s\n",
			telegram.EscapeMD(strings.ReplaceAll(strings.ToLower(a.TradeHistoryPartialHint), "_", " ")))
	}

	fmt.Fprintf(&b, "\nNote: %s\n",
		telegram.EscapeMD("suspicious informed-flow candidate, not a legal insider claim"))
	fmt.Fprintf(&b, "\nWhat happens next:\n· admin review recommended\n· wallet can be manually promoted to watchlist if confirmed\n\n")

	dashLink := ""
	if a.DashboardURL != "" {
		dashLink = mdLinkEscaped("Dashboard", a.DashboardURL)
	}
	emitLinks(&b, JoinLinks(
		links.TraderLink("Trader", a.ProfileSlug, a.WalletFull),
		dashLink,
	))
	return b.String()
}

// FormatWalletDemoted renders the admin-only demotion notice.
func FormatWalletDemoted(a DiscoveryAlert, links LinkBuilder) string {
	var b strings.Builder
	title := "*WALLET DEMOTED*"
	if a.StreakState == "broken" {
		title = "*INSIDER STREAK BROKEN*"
	}
	fmt.Fprintf(&b, "%s · *ADMIN*\n\n", title)
	walletLink := links.TraderLink(a.WalletShort, a.ProfileSlug, a.WalletFull)
	if walletLink == "" {
		fmt.Fprintf(&b, "Wallet: %s\n", telegram.EscapeMD(a.WalletShort))
	} else {
		fmt.Fprintf(&b, "Wallet: %s\n", walletLink)
	}
	fmt.Fprintf(&b, "Class: %s\n", telegram.EscapeMD(a.Class))
	fmt.Fprintf(&b, "Status: %s\n", telegram.EscapeMD(a.Status))
	if a.ReasonHumanized != "" {
		fmt.Fprintf(&b, "Why: %s\n", telegram.EscapeMD(a.ReasonHumanized))
	}
	if a.Class == "insider_like" {
		fmt.Fprintf(&b, "Streak: %s/%s\n",
			telegram.EscapeMD(fmt.Sprintf("%d", a.ClosedWins)),
			telegram.EscapeMD(fmt.Sprintf("%d", a.ClosedLosses)))
	}
	fmt.Fprintf(&b, "Note: future user\\-facing alerts for this wallet are suppressed\\.\n")
	emitLinks(&b, JoinLinks(
		links.TraderLink("Trader", a.ProfileSlug, a.WalletFull),
		links.MarketLink("Market", a.MarketSlug),
		links.EventLink("Event", a.EventSlug),
	))
	return b.String()
}

// FormatSharkFalsePositiveCorrected renders the admin-only correction notice
// when a wallet was promoted as an active shark but veto evidence (strongly
// negative all-time P&L, missing risk metrics, oversized open-position ratio)
// contradicts the local closed-position sample. Sent to the admin channel only.
func FormatSharkFalsePositiveCorrected(a DiscoveryAlert, links LinkBuilder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*SHARK DEMOTED · FALSE POSITIVE CORRECTED* · *ADMIN*\n\n")

	name := a.Pseudonym
	if name == "" {
		name = a.WalletShort
	}
	walletLink := links.TraderLink(name, a.ProfileSlug, a.WalletFull)
	if walletLink == "" {
		fmt.Fprintf(&b, "Trader: %s\n", telegram.EscapeMD(name))
	} else {
		fmt.Fprintf(&b, "Trader: %s\n", walletLink)
	}
	fmt.Fprintf(&b, "Previous class: %s\n", telegram.EscapeMD(a.Class))
	fmt.Fprintf(&b, "New status: rejected\n")

	if a.VetoReason != "" {
		fmt.Fprintf(&b, "\nReason:\n")
		switch a.VetoReason {
		case "MASSIVE_NEGATIVE_ALL_TIME_PNL":
			fmt.Fprintf(&b, "· massive negative all\\-time P/L\n")
		case "NEGATIVE_ALL_TIME_PNL":
			fmt.Fprintf(&b, "· negative all\\-time P/L\n")
		case "PROFILE_PNL_CONTRADICTS_LOCAL_MASSIVE", "PROFILE_PNL_CONTRADICTS_LOCAL":
			fmt.Fprintf(&b, "· local closed\\-position sample contradicted profile P/L\n")
		case "MISSING_RISK_QUALITY_METRICS_PARTIAL_DATA":
			fmt.Fprintf(&b, "· missing risk quality metrics \\(profit factor, avg win/loss\\) with partial data\n")
		case "REALIZED_SAMPLE_TOO_SMALL_FOR_POSITION_UNIVERSE":
			fmt.Fprintf(&b, "· realized sample too small for position universe\n")
		}
		fmt.Fprintf(&b, "· partial data cannot active\\-promote shark\n")
	}

	fmt.Fprintf(&b, "\nStats:\n")
	if a.VetoProfileCashPnLKnown {
		fmt.Fprintf(&b, "· profile all\\-time P/L: %s\n", telegram.EscapeMD(usd(a.VetoProfileCashPnL)))
	}
	if a.VetoLocalPnL != 0 {
		fmt.Fprintf(&b, "· local realized sample PnL: %s\n", telegram.EscapeMD(usd(a.VetoLocalPnL)))
	}
	if a.RealizedCyclesCheck > 0 {
		fmt.Fprintf(&b, "· realized cycles checked: %d\n", a.RealizedCyclesCheck)
	}
	if a.PositionsChecked > 0 {
		fmt.Fprintf(&b, "· positions checked: %d\n", a.PositionsChecked)
	}
	if a.OpenUnresolvedCount > 0 {
		fmt.Fprintf(&b, "· open/unresolved excluded: %d\n", a.OpenUnresolvedCount)
	}
	if a.VetoOpenPositionRatio > 0 {
		fmt.Fprintf(&b, "· open position ratio: %.0f%%\n", a.VetoOpenPositionRatio*100)
	}
	if a.RealizedProfitFactor == 0 {
		fmt.Fprintf(&b, "· profit factor: n/a\n")
	}
	if a.AvgWinUSD == 0 && a.AvgLossUSD == 0 {
		fmt.Fprintf(&b, "· avg win/loss: n/a\n")
	}

	fmt.Fprintf(&b, "\nWhat changed:\n")
	fmt.Fprintf(&b, "· wallet removed from active watchlist\n")
	fmt.Fprintf(&b, "· future SHARK_BET alerts suppressed unless rescore passes strict gates\n")

	emitLinks(&b, JoinLinks(
		links.TraderLink("Trader", a.ProfileSlug, a.WalletFull),
	))
	return b.String()
}

func streakLabel(s string) string {
	switch s {
	case "continues":
		return "clean streak continues"
	case "broken":
		return "streak just broken"
	case "clean":
		return "clean (no losses yet)"
	default:
		return "n/a"
	}
}

// FormatClusterAlert renders the CLUSTER BET DETECTED message.
func FormatClusterAlert(a ClusterAlert, links LinkBuilder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*CLUSTER BET DETECTED* · *HIGH*\n\n")
	clusterDirLabel := string(a.Direction)
	if a.DirectionOutcome != "" {
		clusterDirLabel = string(a.Direction) + " (" + a.DirectionOutcome + ")"
	}
	fmt.Fprintf(&b, "Direction: %s\n", telegram.EscapeMD(clusterDirLabel))
	fmt.Fprintf(&b, "Market: %s\n", telegram.EscapeMD(a.MarketTitle))
	if a.EventTitle != "" {
		fmt.Fprintf(&b, "Event: %s\n", telegram.EscapeMD(a.EventTitle))
	}
	fmt.Fprintf(&b, "Total size: %s · Wallets: %s\n",
		telegram.EscapeMD(usd(a.TotalNotional)),
		telegram.EscapeMD(fmt.Sprintf("%d watched traders", a.WalletCount)))
	fmt.Fprintf(&b, "Weighted price: %s · Odds: %s · Payoff if win: %s\n",
		telegram.EscapeMD(priceCents(a.WeightedPrice)),
		telegram.EscapeMD(ratioX(a.AverageOdds)),
		telegram.EscapeMD(usd(a.PayoffIfWin)))
	fmt.Fprintf(&b, "Window: %s\n", telegram.EscapeMD(fmtSecs(a.WindowSeconds)))
	if len(a.Traders) > 0 {
		names := make([]string, 0, len(a.Traders))
		for _, t := range a.Traders {
			n := displayName(t.Pseudonym, t.WalletShort)
			if link := links.TraderLink(n, t.ProfileSlug, t.WalletFull); link != "" {
				names = append(names, link)
			} else {
				names = append(names, telegram.EscapeMD(n))
			}
		}
		fmt.Fprintf(&b, "Traders: %s\n", strings.Join(names, ", "))
	}
	if len(a.ReasonCodes) > 0 {
		fmt.Fprintf(&b, "Why: %s\n", telegram.EscapeMD(strings.Join(a.ReasonCodes, " · ")))
	}
	emitLinks(&b, JoinLinks(
		links.EventLink("Event", a.EventSlug),
		links.MarketLink("Market", a.MarketSlug),
	))
	return b.String()
}

// FormatExitAlert renders POSITION EXIT for both shark and insider_like.
// For insider_like, the message includes the explicit "lifecycle update for
// suspicious informed-flow candidate, not a legal insider claim" disclaimer.
func FormatExitAlert(a ExitAlert, links LinkBuilder) string {
	var b strings.Builder
	sev := a.Severity
	if sev == "" {
		sev = "INFO"
	}
	title := "*POSITION EXIT*"
	if a.Class == "insider_like" {
		title = "*INSIDER\\-LIKE POSITION EXIT*"
	}
	fmt.Fprintf(&b, "%s · *%s*\n\n", title, telegram.EscapeMD(sev))
	if a.Class == "insider_like" {
		walletLink := links.TraderLink(a.WalletShort, a.ProfileSlug, a.WalletFull)
		if walletLink == "" {
			fmt.Fprintf(&b, "Wallet: %s\n", telegram.EscapeMD(a.WalletShort))
		} else {
			fmt.Fprintf(&b, "Wallet: %s\n", walletLink)
		}
	} else {
		traderLink := links.TraderLink(displayName(a.Pseudonym, a.WalletShort), a.ProfileSlug, a.WalletFull)
		if traderLink == "" {
			fmt.Fprintf(&b, "Trader: %s\n", telegram.EscapeMD(displayName(a.Pseudonym, a.WalletShort)))
		} else {
			fmt.Fprintf(&b, "Trader: %s\n", traderLink)
		}
		fmt.Fprintf(&b, "Class: %s\n", telegram.EscapeMD(a.Class))
	}
	fmt.Fprintf(&b, "Market: %s\n", telegram.EscapeMD(a.MarketTitle))
	fmt.Fprintf(&b, "Position: %s\n", telegram.EscapeMD(a.Outcome))
	fmt.Fprintf(&b, "Entry: %s @ %s\n",
		telegram.EscapeMD(usd(a.EntryNotional)),
		telegram.EscapeMD(priceCents(a.EntryPrice)))
	fmt.Fprintf(&b, "Exit: %s @ %s\n",
		telegram.EscapeMD(usd(a.ExitNotional)),
		telegram.EscapeMD(priceCents(a.ExitPrice)))
	statusReadable := strings.ReplaceAll(a.Status, "_", " ")
	fmt.Fprintf(&b, "Status: %s\n", telegram.EscapeMD(statusReadable))
	pnl := "n/a"
	if a.PnLKnown {
		sign := ""
		if a.PnLEstimate > 0 {
			sign = "+"
		}
		pnl = sign + usd(a.PnLEstimate)
	}
	fmt.Fprintf(&b, "Est\\. PnL: %s\n", telegram.EscapeMD(pnl))
	if a.HeldDuration != "" {
		fmt.Fprintf(&b, "Held: %s\n", telegram.EscapeMD(a.HeldDuration))
	}
	if a.Class == "insider_like" {
		fmt.Fprintf(&b, "Note: %s\n",
			telegram.EscapeMD("lifecycle update for suspicious informed-flow candidate, not a legal insider claim"))
	} else {
		fmt.Fprintf(&b, "Why: %s\n", telegram.EscapeMD("watched trader reduced/closed previously alerted position"))
	}
	emitLinks(&b, JoinLinks(
		links.MarketLink("Market", a.MarketSlug),
		links.EventLink("Event", a.EventSlug),
		links.TraderLink("Trader", a.ProfileSlug, a.WalletFull),
	))
	return b.String()
}

// FormatBurstAlert renders SHARK BURST DETECTED (single-trader cluster).
// Used when one watched wallet places multiple entries in a short window.
func FormatBurstAlert(a BurstAlert, links LinkBuilder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*SHARK BURST DETECTED* · *WARNING*\n\n")
	traderLink := links.TraderLink(displayName(a.Pseudonym, a.WalletShort), a.ProfileSlug, a.WalletFull)
	if traderLink == "" {
		fmt.Fprintf(&b, "Trader: %s\n", telegram.EscapeMD(displayName(a.Pseudonym, a.WalletShort)))
	} else {
		fmt.Fprintf(&b, "Trader: %s\n", traderLink)
	}
	if a.Class != "" {
		fmt.Fprintf(&b, "Class: %s\n", telegram.EscapeMD(a.Class))
	}
	fmt.Fprintf(&b, "Bets: %s · Markets: %s\n",
		telegram.EscapeMD(fmt.Sprintf("%d", a.BetsCount)),
		telegram.EscapeMD(fmt.Sprintf("%d", a.DistinctMarkets)))
	fmt.Fprintf(&b, "Total size: %s · Weighted price: %s\n",
		telegram.EscapeMD(usd(a.TotalNotional)),
		telegram.EscapeMD(priceCents(a.WeightedPrice)))
	fmt.Fprintf(&b, "Window: %s\n",
		telegram.EscapeMD(fmt.Sprintf("%dm", a.WindowMinutes)))
	if len(a.Markets) > 0 {
		maxLines := 5
		for i, m := range a.Markets {
			if i >= maxLines {
				fmt.Fprintf(&b, "… and %s more\n",
					telegram.EscapeMD(fmt.Sprintf("%d", len(a.Markets)-maxLines)))
				break
			}
			label := m.Title
			if label == "" {
				label = m.Slug
			}
			marketLink := links.MarketLink(truncTitle(label, 40), m.Slug)
			if marketLink == "" {
				marketLink = telegram.EscapeMD(truncTitle(label, 40))
			}
			fmt.Fprintf(&b, "· %s %s — %s \\(%dx\\)\n",
				telegram.EscapeMD(string(m.Direction)),
				marketLink,
				telegram.EscapeMD(usd(m.Notional)),
				m.BetCount)
		}
	}
	fmt.Fprintf(&b, "Why: %s\n",
		telegram.EscapeMD("rapid same-wallet activity by watched trader"))
	emitLinks(&b, JoinLinks(
		links.TraderLink("Trader", a.ProfileSlug, a.WalletFull),
	))
	return b.String()
}

func truncTitle(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// FormatNewsAlert renders the POLYMARKET EVENT NEWS message.
func FormatNewsAlert(a NewsAlert, links LinkBuilder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*POLYMARKET EVENT NEWS*\n\n")
	fmt.Fprintf(&b, "Event: %s\n", telegram.EscapeMD(a.EventTitle))
	fmt.Fprintf(&b, "News: %s\n", telegram.EscapeMD(a.Title))
	if a.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", telegram.EscapeMD(a.Summary))
	}
	if !a.Time.IsZero() {
		fmt.Fprintf(&b, "Time: %s\n", telegram.EscapeMD(a.Time.UTC().Format("2006-01-02 15:04 UTC")))
	}
	sourceLink := ""
	if a.SourceURL != "" {
		sourceLink = mdLinkEscaped("Source", a.SourceURL)
	}
	emitLinks(&b, JoinLinks(
		links.EventLink("Event", a.EventSlug),
		sourceLink,
	))
	return b.String()
}

// FormatMLBLateGameAlert renders the late-game baseball timing alert.
func FormatMLBLateGameAlert(a MLBLateGameAlert, links LinkBuilder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*MLB LATE\\-GAME SETUP* · *HIGH*\n\n")
	fmt.Fprintf(&b, "Game: %s at %s\n",
		telegram.EscapeMD(a.AwayTeam),
		telegram.EscapeMD(a.HomeTeam))
	fmt.Fprintf(&b, "Score: %s %d · %s %d \\(%s trailing by %d\\)\n",
		telegram.EscapeMD(a.AwayTeam), a.AwayScore,
		telegram.EscapeMD(a.HomeTeam), a.HomeScore,
		telegram.EscapeMD(a.AwayTeam), a.Deficit)
	state := a.InningState
	if state == "" {
		state = fmt.Sprintf("%s %d", a.InningHalf, a.Inning)
	}
	fmt.Fprintf(&b, "State: %s · status %s\n",
		telegram.EscapeMD(state),
		telegram.EscapeMD(a.Status))
	if !a.GameTime.IsZero() {
		fmt.Fprintf(&b, "Game time: %s\n", telegram.EscapeMD(a.GameTime.UTC().Format("2006-01-02 15:04 UTC")))
	}
	fmt.Fprintf(&b, "\nWhy: away team is batting in top 9\\+/extras while down by %d runs; failed inning can sharply reprice comeback markets\\.\n", a.Deficit)
	if len(a.MatchedMarkets) > 0 {
		fmt.Fprintf(&b, "\nMatched Polymarket markets:\n")
		for i, m := range a.MatchedMarkets {
			if i >= 4 {
				fmt.Fprintf(&b, "… and %s more\n", telegram.EscapeMD(fmt.Sprintf("%d", len(a.MatchedMarkets)-i)))
				break
			}
			title := m.Title
			if title == "" {
				title = m.Slug
			}
			marketLink := links.MarketLink(truncTitle(title, 54), m.Slug)
			if marketLink == "" {
				marketLink = telegram.EscapeMD(truncTitle(title, 54))
			}
			fmt.Fprintf(&b, "· %s\n", marketLink)
		}
	}
	if len(a.ReasonCodes) > 0 {
		fmt.Fprintf(&b, "Reasons: %s\n", telegram.EscapeMD(strings.Join(a.ReasonCodes, " · ")))
	}
	emitLinks(&b, JoinLinks(
		links.EventLink("Event", a.EventSlug),
		links.MarketLink("Market", a.MarketSlug),
	))
	return b.String()
}

func displayName(pseudonym, wallet string) string {
	if pseudonym != "" {
		return pseudonym
	}
	return wallet
}

func scoreGrade(s int) string {
	switch {
	case s >= 90:
		return "S"
	case s >= 80:
		return "A"
	case s >= 70:
		return "B"
	case s >= 60:
		return "C"
	}
	return "D"
}

func fmtSecs(secs int) string {
	if secs%3600 == 0 {
		return fmt.Sprintf("%dh", secs/3600)
	}
	return fmt.Sprintf("%dm", secs/60)
}

// HumanizeReasons turns reason codes into a short comma-separated phrase.
func HumanizeReasons(codes []string, maxN int) string {
	if maxN <= 0 {
		maxN = 4
	}
	humanized := map[string]string{
		"HIGH_SAMPLE_SIZE":               "deep history",
		"POSITIVE_REALIZED_PNL":          "positive realized PnL",
		"STRONG_PAYOFF_PROFILE":          "strong payoff profile",
		"POSITIVE_CLV_PROXY":             "positive post-trade drift",
		"CATEGORY_FOCUSED":               "category focus",
		"LARGE_BET_SELECTIVE":            "selective large bets",
		"RECENT_ACTIVITY":                "recent activity",
		"TOP_HOLDER":                     "top holder",
		"NEW_WALLET":                     "new wallet",
		"LOW_LIFETIME_HISTORY":           "low lifetime history",
		"FIRST_LARGE_BET":                "first large bet",
		"LARGE_NOTIONAL":                 "unusually large bet",
		"LOW_PROBABILITY_ENTRY":          "low-probability entry",
		"HIGH_PAYOFF":                    "high payoff if right",
		"TOP_HOLDER_POSITION":            "top holder",
		"SUDDEN_HOLDER_CONCENTRATION":    "sudden concentration",
		"NEAR_CATALYST":                  "near catalyst",
		"HIGH_IMPACT_MARKET":             "high-impact market",
		"SUSPICIOUS_STREAK":              "suspicious streak",
		"MULTI_WALLET_ALIGNED":           "watched wallets aligned",
		"INSIDER_LIKE_PARTICIPANT":       "insider-like participant",
		"SHARK_PARTICIPANT":              "shark participant",
		"CLUSTER_EVIDENCE":               "cluster alignment",
		"HIGH_FREQUENCY_ACTIVITY":        "high trading frequency",
		"LOW_FREQUENCY_ACTIVITY":         "low trading frequency",
		"WEEKLY_HIGH_FREQUENCY":          "very high weekly frequency",
		"WEEKLY_SUSTAINED_ACTIVITY":      "sustained week-long activity",
		"WEEKLY_REALIZED_SAMPLE_OK":      "sufficient realized sample",
		"WEEKLY_PROFIT_PCT_ABOVE_30PCT":  "weekly profit above 30%",
		"MONTHLY_PROFIT_PCT_ABOVE_30PCT": "monthly profit above 30%",
		"WINDOW_PROFIT_PCT_ABOVE_30PCT":  "window profit above 30%",
		"SUSPECTED_LUCK_SPIKE_PATTERN":   "suspicious weekly luck spike",
	}
	var picked []string
	for _, c := range codes {
		if h, ok := humanized[c]; ok {
			picked = append(picked, h)
		}
		if len(picked) >= maxN {
			break
		}
	}
	return strings.Join(picked, " · ")
}
