package marketscan

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Borislavv/polymarket-sharks/internal/alerts"
	"github.com/Borislavv/polymarket-sharks/internal/metrics"
	"github.com/Borislavv/polymarket-sharks/internal/polymarket"
	"github.com/Borislavv/polymarket-sharks/internal/storage/postgres"
)

// MLBStatsClient reads the public MLB Stats API schedule/linescore endpoint.
type MLBStatsClient struct {
	HTTP *polymarket.HTTPClient
}

func NewMLBStatsClient(http *polymarket.HTTPClient) *MLBStatsClient {
	return &MLBStatsClient{HTTP: http}
}

type mlbScheduleResponse struct {
	Dates []struct {
		Games []MLBGame `json:"games"`
	} `json:"dates"`
}

type MLBGame struct {
	GamePK   int       `json:"gamePk"`
	GameDate time.Time `json:"gameDate"`
	Status   struct {
		DetailedState string `json:"detailedState"`
	} `json:"status"`
	Teams struct {
		Away MLBGameTeam `json:"away"`
		Home MLBGameTeam `json:"home"`
	} `json:"teams"`
	Linescore struct {
		CurrentInning        int    `json:"currentInning"`
		CurrentInningOrdinal string `json:"currentInningOrdinal"`
		InningHalf           string `json:"inningHalf"`
		IsTopInning          bool   `json:"isTopInning"`
	} `json:"linescore"`
}

type MLBGameTeam struct {
	Score *int `json:"score"`
	Team  struct {
		Name string `json:"name"`
	} `json:"team"`
}

func (c *MLBStatsClient) Schedule(ctx context.Context, date time.Time) ([]MLBGame, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("mlb stats client is nil")
	}
	q := url.Values{}
	q.Set("sportId", "1")
	q.Set("hydrate", "linescore")
	q.Set("date", date.Format("2006-01-02"))
	u := strings.TrimRight(c.HTTP.Base, "/") + "/api/v1/schedule?" + q.Encode()
	raw, err := c.HTTP.GET(ctx, u)
	if err != nil {
		return nil, err
	}
	var resp mlbScheduleResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("mlb schedule parse: %w", err)
	}
	var out []MLBGame
	for _, d := range resp.Dates {
		out = append(out, d.Games...)
	}
	return out, nil
}

// MLBLateGameWorker finds MLB games where the away team is batting in a final
// chance top-9/top-extra situation while trailing by at least N runs.
type MLBLateGameWorker struct {
	MLB    *MLBStatsClient
	Store  *postgres.Store
	Router *alerts.Router
	Log    *slog.Logger
	Links  alerts.LinkBuilder

	Enabled        bool
	Interval       time.Duration
	MinInning      int
	MinAwayDeficit int
	MarketLimit    int
}

func (w *MLBLateGameWorker) defaults() {
	if w.Interval <= 0 {
		w.Interval = 30 * time.Second
	}
	if w.MinInning <= 0 {
		w.MinInning = 9
	}
	if w.MinAwayDeficit <= 0 {
		w.MinAwayDeficit = 2
	}
}

func (w *MLBLateGameWorker) Run(ctx context.Context) error {
	w.defaults()
	if !w.Enabled {
		return nil
	}
	w.runOnce(ctx)
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.runOnce(ctx)
		}
	}
}

func (w *MLBLateGameWorker) runOnce(ctx context.Context) {
	if w.MLB == nil || w.Store == nil || w.Router == nil {
		return
	}
	start := time.Now()
	games, err := w.fetchCandidateDates(ctx, start)
	if err != nil {
		metrics.Inc("mlb_late_game_errors_total{stage=scoreboard}")
		if w.Log != nil {
			w.Log.Warn("mlb late-game scoreboard", "err", err)
		}
		return
	}
	markets, err := w.Store.ListActiveMarketsWithEvents(ctx, w.MarketLimit)
	if err != nil {
		metrics.Inc("mlb_late_game_errors_total{stage=markets}")
		if w.Log != nil {
			w.Log.Warn("mlb late-game markets", "err", err)
		}
		return
	}
	var matches, alerted int
	for _, game := range games {
		signal, ok := w.evaluateGame(game)
		if !ok {
			continue
		}
		matchedMarkets := matchMLBMarkets(signal, markets)
		if len(matchedMarkets) == 0 {
			metrics.Inc("mlb_late_game_no_market_match_total")
			continue
		}
		matches++
		if w.emitAlert(ctx, signal, matchedMarkets) {
			alerted++
		}
	}
	metrics.Add("mlb_late_game_matches_total", int64(matches))
	metrics.Add("mlb_late_game_alerts_sent_total", int64(alerted))
	if w.Log != nil {
		w.Log.Info("mlb late-game cycle completed",
			"games_seen", len(games),
			"matches", matches,
			"alerts_sent", alerted,
			"markets_scanned", len(markets),
			"duration", time.Since(start).String())
	}
}

func (w *MLBLateGameWorker) fetchCandidateDates(ctx context.Context, now time.Time) ([]MLBGame, error) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	ny := now.In(loc)
	dates := []time.Time{ny.AddDate(0, 0, -1), ny, ny.AddDate(0, 0, 1)}
	seen := map[int]struct{}{}
	var out []MLBGame
	for _, d := range dates {
		games, err := w.MLB.Schedule(ctx, d)
		if err != nil {
			return out, err
		}
		for _, g := range games {
			if _, ok := seen[g.GamePK]; ok {
				continue
			}
			seen[g.GamePK] = struct{}{}
			out = append(out, g)
		}
	}
	return out, nil
}

type MLBLateGameSignal struct {
	GamePK      int
	AwayTeam    string
	HomeTeam    string
	AwayScore   int
	HomeScore   int
	Deficit     int
	Inning      int
	InningHalf  string
	InningState string
	Status      string
	GameTime    time.Time
	ReasonCodes []string
}

func (w *MLBLateGameWorker) evaluateGame(g MLBGame) (MLBLateGameSignal, bool) {
	if g.Teams.Away.Score == nil || g.Teams.Home.Score == nil {
		return MLBLateGameSignal{}, false
	}
	if !g.Linescore.IsTopInning || !strings.EqualFold(g.Linescore.InningHalf, "Top") {
		return MLBLateGameSignal{}, false
	}
	if g.Linescore.CurrentInning < w.MinInning {
		return MLBLateGameSignal{}, false
	}
	awayScore := *g.Teams.Away.Score
	homeScore := *g.Teams.Home.Score
	deficit := homeScore - awayScore
	if deficit < w.MinAwayDeficit {
		return MLBLateGameSignal{}, false
	}
	status := strings.TrimSpace(g.Status.DetailedState)
	if !isLiveMLBState(status) {
		return MLBLateGameSignal{}, false
	}
	state := strings.TrimSpace(g.Linescore.CurrentInningOrdinal)
	if state != "" && g.Linescore.InningHalf != "" {
		state = strings.TrimSpace(g.Linescore.InningHalf + " " + state)
	}
	if state == "" {
		state = fmt.Sprintf("%s %d", strings.TrimSpace(g.Linescore.InningHalf), g.Linescore.CurrentInning)
	}
	reasons := []string{
		"MLB_TOP_9_PLUS",
		fmt.Sprintf("AWAY_TEAM_TRAILING_BY_%d_PLUS", w.MinAwayDeficit),
		"FINAL_CHANCE_AT_BAT",
	}
	if g.Linescore.CurrentInning > 9 {
		reasons = append(reasons, "EXTRA_INNINGS")
	}
	return MLBLateGameSignal{
		GamePK:      g.GamePK,
		AwayTeam:    g.Teams.Away.Team.Name,
		HomeTeam:    g.Teams.Home.Team.Name,
		AwayScore:   awayScore,
		HomeScore:   homeScore,
		Deficit:     deficit,
		Inning:      g.Linescore.CurrentInning,
		InningHalf:  g.Linescore.InningHalf,
		InningState: state,
		Status:      status,
		GameTime:    g.GameDate,
		ReasonCodes: reasons,
	}, true
}

func isLiveMLBState(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return false
	}
	blocked := []string{"scheduled", "pre-game", "warmup", "final", "game over", "postponed", "cancelled", "canceled", "delayed"}
	for _, b := range blocked {
		if strings.Contains(s, b) {
			return false
		}
	}
	return true
}

func (w *MLBLateGameWorker) emitAlert(ctx context.Context, s MLBLateGameSignal, markets []postgres.MarketSummary) bool {
	first := markets[0]
	matched := make([]alerts.MLBMatchedMarket, 0, len(markets))
	for _, m := range markets {
		matched = append(matched, alerts.MLBMatchedMarket{
			Slug:       m.Slug,
			Title:      m.Question,
			EventSlug:  m.EventSlug,
			EventTitle: m.EventTitle,
		})
	}
	dedup := alerts.DedupKey(alerts.TypeMLBLateGame,
		fmt.Sprintf("%d", s.GamePK), fmt.Sprintf("%d", s.Inning),
		s.InningHalf, fmt.Sprintf("%d-%d", s.AwayScore, s.HomeScore))
	decision := postgres.AlertDecision{
		AlertType:         alerts.TypeMLBLateGame,
		EntityType:        "mlb_game",
		EntityID:          fmt.Sprintf("%d", s.GamePK),
		Severity:          "HIGH",
		ShouldSend:        true,
		UserAlertAllowed:  false,
		AdminAlertAllowed: true,
		ReasonCodes:       s.ReasonCodes,
		FeatureSnapshot: map[string]any{
			"game_pk":         s.GamePK,
			"away_team":       s.AwayTeam,
			"home_team":       s.HomeTeam,
			"away_score":      s.AwayScore,
			"home_score":      s.HomeScore,
			"away_deficit":    s.Deficit,
			"inning":          s.Inning,
			"inning_half":     s.InningHalf,
			"inning_state":    s.InningState,
			"status":          s.Status,
			"matched_markets": len(markets),
			"source":          "mlb_stats_api",
		},
		DedupKey: dedup,
	}
	body := alerts.FormatMLBLateGameAlert(alerts.MLBLateGameAlert{
		GamePK:         s.GamePK,
		AwayTeam:       s.AwayTeam,
		HomeTeam:       s.HomeTeam,
		AwayScore:      s.AwayScore,
		HomeScore:      s.HomeScore,
		Deficit:        s.Deficit,
		Inning:         s.Inning,
		InningHalf:     s.InningHalf,
		InningState:    s.InningState,
		Status:         s.Status,
		GameTime:       s.GameTime,
		MarketSlug:     first.Slug,
		MarketTitle:    first.Question,
		EventSlug:      first.EventSlug,
		EventTitle:     first.EventTitle,
		MatchedMarkets: matched,
		ReasonCodes:    s.ReasonCodes,
		DedupKey:       dedup,
	}, w.Links)
	out := w.Router.Route(ctx, decision, body, alerts.ChannelAdmin)
	if out.Err != nil && w.Log != nil {
		w.Log.Warn("mlb late-game alert send", "game_pk", s.GamePK, "err", out.Err)
	}
	return out.Sent
}

func matchMLBMarkets(s MLBLateGameSignal, markets []postgres.MarketSummary) []postgres.MarketSummary {
	awayAliases := teamAliases(s.AwayTeam)
	homeAliases := teamAliases(s.HomeTeam)
	var out []postgres.MarketSummary
	for _, m := range markets {
		text := normalizeMatchText(strings.Join([]string{
			m.EventTitle, m.Question, m.Slug, m.EventSlug,
		}, " "))
		if !looksLikeGameMarket(text) || hasFuturesKeyword(text) {
			continue
		}
		if containsAnyAlias(text, awayAliases) && containsAnyAlias(text, homeAliases) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Liquidity+out[i].Volume > out[j].Liquidity+out[j].Volume
	})
	return out
}

func looksLikeGameMarket(text string) bool {
	return strings.Contains(text, " mlb ") ||
		strings.Contains(text, " baseball ") ||
		strings.Contains(text, " vs ") ||
		strings.Contains(text, " at ")
}

func hasFuturesKeyword(text string) bool {
	keywords := []string{
		"regular season", "longest", "manager", "home run", "homer", "world series",
		"pennant", "division", "playoff", "mvp", "cy young", "rookie", "award",
		"win streak", "most wins", "make the playoffs",
	}
	for _, k := range keywords {
		if strings.Contains(text, k) {
			return true
		}
	}
	return false
}

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeMatchText(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = nonAlphaNum.ReplaceAllString(s, " ")
	return " " + strings.Join(strings.Fields(s), " ") + " "
}

func containsAnyAlias(text string, aliases []string) bool {
	for _, a := range aliases {
		if strings.Contains(text, " "+a+" ") {
			return true
		}
	}
	return false
}

func teamAliases(name string) []string {
	n := strings.ToLower(strings.TrimSpace(name))
	if aliases, ok := mlbTeamAliases[n]; ok {
		return aliases
	}
	parts := strings.Fields(n)
	if len(parts) == 0 {
		return nil
	}
	out := []string{normalizeAlias(n), normalizeAlias(parts[len(parts)-1])}
	return uniqueAliases(out)
}

func normalizeAlias(s string) string {
	return strings.TrimSpace(strings.Trim(normalizeMatchText(s), " "))
}

func uniqueAliases(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, a := range in {
		a = normalizeAlias(a)
		if a == "" {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	return out
}

var mlbTeamAliases = map[string][]string{
	"arizona diamondbacks":  uniqueAliases([]string{"arizona diamondbacks", "diamondbacks", "d-backs", "dbacks", "ari"}),
	"atlanta braves":        uniqueAliases([]string{"atlanta braves", "braves", "atl"}),
	"baltimore orioles":     uniqueAliases([]string{"baltimore orioles", "orioles", "bal"}),
	"boston red sox":        uniqueAliases([]string{"boston red sox", "red sox", "boston", "bos"}),
	"chicago cubs":          uniqueAliases([]string{"chicago cubs", "cubs", "chc"}),
	"chicago white sox":     uniqueAliases([]string{"chicago white sox", "white sox", "chw", "cws"}),
	"cincinnati reds":       uniqueAliases([]string{"cincinnati reds", "reds", "cin"}),
	"cleveland guardians":   uniqueAliases([]string{"cleveland guardians", "guardians", "cle"}),
	"colorado rockies":      uniqueAliases([]string{"colorado rockies", "rockies", "col"}),
	"detroit tigers":        uniqueAliases([]string{"detroit tigers", "tigers", "det"}),
	"houston astros":        uniqueAliases([]string{"houston astros", "astros", "hou"}),
	"kansas city royals":    uniqueAliases([]string{"kansas city royals", "royals", "kc", "kcr"}),
	"los angeles angels":    uniqueAliases([]string{"los angeles angels", "la angels", "angels", "anaheim angels", "laa"}),
	"los angeles dodgers":   uniqueAliases([]string{"los angeles dodgers", "la dodgers", "dodgers", "lad"}),
	"miami marlins":         uniqueAliases([]string{"miami marlins", "marlins", "mia"}),
	"milwaukee brewers":     uniqueAliases([]string{"milwaukee brewers", "brewers", "mil"}),
	"minnesota twins":       uniqueAliases([]string{"minnesota twins", "twins", "min"}),
	"new york mets":         uniqueAliases([]string{"new york mets", "ny mets", "mets", "nym"}),
	"new york yankees":      uniqueAliases([]string{"new york yankees", "ny yankees", "yankees", "nyy"}),
	"athletics":             uniqueAliases([]string{"athletics", "a's", "oakland athletics", "oakland a's", "sacramento athletics", "sacramento a's", "oak", "sac", "ath"}),
	"philadelphia phillies": uniqueAliases([]string{"philadelphia phillies", "phillies", "phi"}),
	"pittsburgh pirates":    uniqueAliases([]string{"pittsburgh pirates", "pirates", "pit"}),
	"san diego padres":      uniqueAliases([]string{"san diego padres", "padres", "sd", "sdp"}),
	"san francisco giants":  uniqueAliases([]string{"san francisco giants", "giants", "sf", "sfg"}),
	"seattle mariners":      uniqueAliases([]string{"seattle mariners", "mariners", "sea"}),
	"st. louis cardinals":   uniqueAliases([]string{"st louis cardinals", "st. louis cardinals", "cardinals", "stl"}),
	"tampa bay rays":        uniqueAliases([]string{"tampa bay rays", "rays", "tb", "tbr"}),
	"texas rangers":         uniqueAliases([]string{"texas rangers", "rangers", "tex"}),
	"toronto blue jays":     uniqueAliases([]string{"toronto blue jays", "blue jays", "tor"}),
	"washington nationals":  uniqueAliases([]string{"washington nationals", "nationals", "was", "wsh"}),
}
