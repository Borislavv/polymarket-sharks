package marketscan

import (
	"strings"
	"testing"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

func TestMLBLateGameWorker_EvaluateTopNineAwayTrailingByTwo(t *testing.T) {
	w := &MLBLateGameWorker{MinInning: 9, MinAwayDeficit: 2}
	game := testMLBGame("New York Yankees", "Boston Red Sox", 3, 5, 9, "9th", "Top", true, "In Progress")

	signal, ok := w.evaluateGame(game)
	if !ok {
		t.Fatal("expected late-game signal")
	}
	if signal.Deficit != 2 {
		t.Fatalf("deficit: want 2, got %d", signal.Deficit)
	}
	if signal.InningState != "Top 9th" {
		t.Fatalf("inning state: want Top 9th, got %q", signal.InningState)
	}
	if !hasString(signal.ReasonCodes, "FINAL_CHANCE_AT_BAT") {
		t.Fatalf("expected final-chance reason, got %v", signal.ReasonCodes)
	}
}

func TestMLBLateGameWorker_EvaluateTopExtraAwayTrailing(t *testing.T) {
	w := &MLBLateGameWorker{MinInning: 9, MinAwayDeficit: 2}
	game := testMLBGame("Los Angeles Dodgers", "San Diego Padres", 6, 9, 10, "10th", "Top", true, "In Progress")

	signal, ok := w.evaluateGame(game)
	if !ok {
		t.Fatal("expected extra-inning signal")
	}
	if signal.Inning != 10 {
		t.Fatalf("inning: want 10, got %d", signal.Inning)
	}
	if !hasString(signal.ReasonCodes, "EXTRA_INNINGS") {
		t.Fatalf("expected extra-innings reason, got %v", signal.ReasonCodes)
	}
}

func TestMLBLateGameWorker_RejectsBottomOrSmallDeficit(t *testing.T) {
	w := &MLBLateGameWorker{MinInning: 9, MinAwayDeficit: 2}

	if _, ok := w.evaluateGame(testMLBGame("New York Yankees", "Boston Red Sox", 3, 5, 9, "9th", "Bottom", false, "In Progress")); ok {
		t.Fatal("bottom 9th should not be a signal")
	}
	if _, ok := w.evaluateGame(testMLBGame("New York Yankees", "Boston Red Sox", 4, 5, 9, "9th", "Top", true, "In Progress")); ok {
		t.Fatal("one-run deficit should not be a signal")
	}
	if _, ok := w.evaluateGame(testMLBGame("New York Yankees", "Boston Red Sox", 3, 5, 9, "9th", "Top", true, "Final")); ok {
		t.Fatal("final game should not be a signal")
	}
}

func TestMatchMLBMarkets_MatchesBothTeamsAndFiltersFutures(t *testing.T) {
	signal := MLBLateGameSignal{
		AwayTeam: "New York Yankees",
		HomeTeam: "Boston Red Sox",
	}
	markets := []postgres.MarketSummary{
		{
			Slug:       "mlb-yankees-red-sox-2026-06-02",
			Question:   "MLB: Yankees vs Red Sox",
			EventSlug:  "yankees-red-sox",
			EventTitle: "Yankees at Red Sox",
			Liquidity:  100,
		},
		{
			Slug:       "will-the-yankees-have-the-longest-win-streak",
			Question:   "Will the Yankees have the longest regular season win streak?",
			EventTitle: "MLB futures",
			Volume:     1000,
		},
		{
			Slug:     "mlb-red-sox-mets-2026-06-02",
			Question: "MLB: Red Sox vs Mets",
			Volume:   2000,
		},
	}

	got := matchMLBMarkets(signal, markets)
	if len(got) != 1 {
		t.Fatalf("matched markets: want 1, got %d (%v)", len(got), got)
	}
	if !strings.Contains(got[0].Question, "Yankees") {
		t.Fatalf("unexpected market matched: %+v", got[0])
	}
}

func TestMatchMLBMarkets_MatchesAthleticsOAKSlug(t *testing.T) {
	signal := MLBLateGameSignal{
		AwayTeam: "New York Yankees",
		HomeTeam: "Athletics",
	}
	markets := []postgres.MarketSummary{
		{
			Slug:     "mlb-nyy-oak-2026-06-02-spread-away-1pt5",
			Question: "Spread: New York Yankees (-1.5)",
		},
	}

	got := matchMLBMarkets(signal, markets)
	if len(got) != 1 {
		t.Fatalf("matched markets: want 1, got %d", len(got))
	}
}

func testMLBGame(away, home string, awayScore, homeScore, inning int, ordinal, half string, top bool, status string) MLBGame {
	var g MLBGame
	g.GamePK = 12345
	g.GameDate = time.Date(2026, 6, 2, 23, 5, 0, 0, time.UTC)
	g.Status.DetailedState = status
	g.Teams.Away.Team.Name = away
	g.Teams.Home.Team.Name = home
	g.Teams.Away.Score = &awayScore
	g.Teams.Home.Score = &homeScore
	g.Linescore.CurrentInning = inning
	g.Linescore.CurrentInningOrdinal = ordinal
	g.Linescore.InningHalf = half
	g.Linescore.IsTopInning = top
	return g
}

func hasString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
