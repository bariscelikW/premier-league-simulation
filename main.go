// Premier League Simulation API
// =============================
// Pure stdlib Go (net/http) — zero external dependencies, runs out of the box:
//     go run main.go
//
// Endpoints:
//     GET  /league-table   -> current standings
//     POST /next-week      -> simulate current week, return results + table (+ predictions if week>=4)
//     POST /play-all       -> simulate every remaining week instantly
//     POST /reset          -> reset the season (bonus, for retesting)
//
// Architecture:
//     SimulationEngine ──┬── LeagueRepository  (in-memory; SQL-shaped, see schema.sql)
//                        ├── Simulator         (Poisson model weighted by team Strength)
//                        └── PredictionService (Monte Carlo over remaining fixtures)

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"time"
)

// ============================================================
// DOMAIN MODELS (struct composition: Team + Stats -> TableEntry)
// ============================================================

// Team holds the intrinsic, immutable attributes of a club.
type Team struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Strength int    `json:"strength"` // 1..100 — drives simulation lambdas
}

// Stats holds league-table figures. Kept separate from Team so the same
// Team struct can be embedded in different contexts (table, fixture list, etc.).
type Stats struct {
	Played         int `json:"played"`
	Won            int `json:"won"`
	Drawn          int `json:"drawn"`
	Lost           int `json:"lost"`
	GoalsFor       int `json:"goals_for"`
	GoalsAgainst   int `json:"goals_against"`
	GoalDifference int `json:"goal_difference"`
	Points         int `json:"points"`
}

// LeagueTableEntry composes Team + Stats. Embedded fields are promoted in JSON.
type LeagueTableEntry struct {
	Team
	Stats
	Position int `json:"position"`
}

// Match is a fixture, played or scheduled.
type Match struct {
	ID         int  `json:"id"`
	Week       int  `json:"week"`
	HomeTeamID int  `json:"home_team_id"`
	AwayTeamID int  `json:"away_team_id"`
	HomeGoals  int  `json:"home_goals"`
	AwayGoals  int  `json:"away_goals"`
	Played     bool `json:"played"`
}

// MatchResult is the human-friendly response view of a played match.
type MatchResult struct {
	Week      int    `json:"week"`
	HomeTeam  string `json:"home_team"`
	AwayTeam  string `json:"away_team"`
	HomeGoals int    `json:"home_goals"`
	AwayGoals int    `json:"away_goals"`
}

// Prediction is a team's championship probability (0..100).
type Prediction struct {
	TeamName       string  `json:"team_name"`
	WinProbability float64 `json:"win_probability"`
}

// ============================================================
// INTERFACES
// ============================================================

// Simulator turns two teams into a scoreline.
type Simulator interface {
	Simulate(home, away Team) (homeGoals, awayGoals int)
}

// LeagueRepository hides the storage layer. Swap InMemoryRepository for a
// Postgres-backed one without touching the engine.
type LeagueRepository interface {
	GetTeams() []Team
	GetTeamByID(id int) (Team, bool)
	GetMatchesByWeek(week int) []Match
	GetAllMatches() []Match
	UpdateMatch(m Match)
	CurrentWeek() int
	AdvanceWeek()
	TotalWeeks() int
}

// PredictionService computes championship probabilities from the live state.
type PredictionService interface {
	Predict() []Prediction
}

// ============================================================
// SIMULATOR — Poisson model
// ============================================================

// PoissonSimulator draws goals from a Poisson distribution whose mean (lambda)
// is the team's share of total strength, scaled to ~3 goals/match, with a
// modest home-field boost.
type PoissonSimulator struct {
	rng *rand.Rand
	mu  sync.Mutex // rand.Rand is not safe for concurrent use
}

func NewPoissonSimulator(seed int64) *PoissonSimulator {
	return &PoissonSimulator{rng: rand.New(rand.NewSource(seed))}
}

// poissonSample — Knuth's algorithm. Cheap and exact for the small lambdas
// (~1–2.5) seen in football scorelines.
func (p *PoissonSimulator) poissonSample(lambda float64) int {
	L := math.Exp(-lambda)
	k := 0
	prod := 1.0
	for {
		k++
		prod *= p.rng.Float64()
		if prod <= L {
			return k - 1
		}
	}
}

func (p *PoissonSimulator) Simulate(home, away Team) (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	const totalGoals = 2.8 // expected combined goals
	const homeAdv = 1.20   // ~20% bump for the home side

	share := float64(home.Strength) / float64(home.Strength+away.Strength)
	homeLambda := share * totalGoals * homeAdv
	awayLambda := (1.0 - share) * totalGoals

	return p.poissonSample(homeLambda), p.poissonSample(awayLambda)
}

// ============================================================
// IN-MEMORY REPOSITORY
// ============================================================

type InMemoryRepository struct {
	mu          sync.RWMutex
	teams       []Team
	matches     []Match
	currentWeek int
	totalWeeks  int
}

func NewInMemoryRepository(teams []Team) *InMemoryRepository {
	r := &InMemoryRepository{
		teams:       teams,
		currentWeek: 1,
		totalWeeks:  6, // 4 teams, double round-robin -> 6 weeks
	}
	r.matches = generateFixtures(teams)
	return r
}

// generateFixtures builds a double round-robin for 4 teams (12 matches over 6 weeks).
// Weeks 1-3 are the first leg; weeks 4-6 replay each pairing with home/away swapped.
func generateFixtures(teams []Team) []Match {
	if len(teams) != 4 {
		panic("this fixture generator expects exactly 4 teams")
	}
	t := teams
	pairs := [][2]int{
		{0, 1}, {2, 3}, // week 1
		{0, 2}, {1, 3}, // week 2
		{0, 3}, {1, 2}, // week 3
	}
	var matches []Match
	id := 1
	// First leg
	for i, p := range pairs {
		matches = append(matches, Match{
			ID: id, Week: (i / 2) + 1,
			HomeTeamID: t[p[0]].ID, AwayTeamID: t[p[1]].ID,
		})
		id++
	}
	// Return leg — same pairs, home/away reversed
	for i, p := range pairs {
		matches = append(matches, Match{
			ID: id, Week: (i / 2) + 4,
			HomeTeamID: t[p[1]].ID, AwayTeamID: t[p[0]].ID,
		})
		id++
	}
	return matches
}

func (r *InMemoryRepository) GetTeams() []Team {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Team, len(r.teams))
	copy(out, r.teams)
	return out
}

func (r *InMemoryRepository) GetTeamByID(id int) (Team, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.teams {
		if t.ID == id {
			return t, true
		}
	}
	return Team{}, false
}

func (r *InMemoryRepository) GetMatchesByWeek(week int) []Match {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Match
	for _, m := range r.matches {
		if m.Week == week {
			out = append(out, m)
		}
	}
	return out
}

func (r *InMemoryRepository) GetAllMatches() []Match {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Match, len(r.matches))
	copy(out, r.matches)
	return out
}

func (r *InMemoryRepository) UpdateMatch(m Match) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.matches {
		if r.matches[i].ID == m.ID {
			r.matches[i] = m
			return
		}
	}
}

func (r *InMemoryRepository) CurrentWeek() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.currentWeek
}

func (r *InMemoryRepository) AdvanceWeek() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentWeek <= r.totalWeeks {
		r.currentWeek++
	}
}

func (r *InMemoryRepository) TotalWeeks() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.totalWeeks
}

// ============================================================
// LEAGUE TABLE COMPUTATION (derived from match log)
// ============================================================

func computeTable(repo LeagueRepository) []LeagueTableEntry {
	teams := repo.GetTeams()
	statsByID := make(map[int]*Stats, len(teams))
	for _, t := range teams {
		statsByID[t.ID] = &Stats{}
	}
	for _, m := range repo.GetAllMatches() {
		if !m.Played {
			continue
		}
		applyMatch(statsByID, m)
	}
	entries := make([]LeagueTableEntry, 0, len(teams))
	for _, t := range teams {
		s := *statsByID[t.ID]
		s.GoalDifference = s.GoalsFor - s.GoalsAgainst
		entries = append(entries, LeagueTableEntry{Team: t, Stats: s})
	}
	sortTable(entries)
	for i := range entries {
		entries[i].Position = i + 1
	}
	return entries
}

func sortTable(entries []LeagueTableEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Points != entries[j].Points {
			return entries[i].Points > entries[j].Points
		}
		if entries[i].GoalDifference != entries[j].GoalDifference {
			return entries[i].GoalDifference > entries[j].GoalDifference
		}
		return entries[i].GoalsFor > entries[j].GoalsFor
	})
}

// applyMatch mutates the two teams' Stats according to one match outcome.
// Shared by both the live table and the Monte Carlo simulator.
func applyMatch(stats map[int]*Stats, m Match) {
	home, away := stats[m.HomeTeamID], stats[m.AwayTeamID]
	home.Played++
	away.Played++
	home.GoalsFor += m.HomeGoals
	home.GoalsAgainst += m.AwayGoals
	away.GoalsFor += m.AwayGoals
	away.GoalsAgainst += m.HomeGoals
	switch {
	case m.HomeGoals > m.AwayGoals:
		home.Won++
		home.Points += 3
		away.Lost++
	case m.HomeGoals < m.AwayGoals:
		away.Won++
		away.Points += 3
		home.Lost++
	default:
		home.Drawn++
		away.Drawn++
		home.Points++
		away.Points++
	}
}

// ============================================================
// PREDICTION SERVICE — Monte Carlo
// ============================================================

// MonteCarloPredictor replays the remaining fixtures `runs` times via the
// Simulator and counts how often each team finishes top of the table.
// Naturally captures point margins, GD tie-breakers, and strength advantage.
type MonteCarloPredictor struct {
	repo      LeagueRepository
	simulator Simulator
	runs      int
}

func NewMonteCarloPredictor(repo LeagueRepository, sim Simulator, runs int) *MonteCarloPredictor {
	return &MonteCarloPredictor{repo: repo, simulator: sim, runs: runs}
}

func (p *MonteCarloPredictor) Predict() []Prediction {
	teams := p.repo.GetTeams()
	teamByID := make(map[int]Team, len(teams))
	for _, t := range teams {
		teamByID[t.ID] = t
	}

	var played, unplayed []Match
	for _, m := range p.repo.GetAllMatches() {
		if m.Played {
			played = append(played, m)
		} else {
			unplayed = append(unplayed, m)
		}
	}

	// Season finished — champion is decided; split between any ties at top.
	if len(unplayed) == 0 {
		return finalPredictions(p.repo)
	}

	titleCounts := make(map[int]int, len(teams))
	for run := 0; run < p.runs; run++ {
		stats := make(map[int]*Stats, len(teams))
		for _, t := range teams {
			stats[t.ID] = &Stats{}
		}
		for _, m := range played {
			applyMatch(stats, m)
		}
		for _, m := range unplayed {
			hg, ag := p.simulator.Simulate(teamByID[m.HomeTeamID], teamByID[m.AwayTeamID])
			sim := m
			sim.HomeGoals, sim.AwayGoals = hg, ag
			applyMatch(stats, sim)
		}
		titleCounts[championOf(stats)]++
	}

	out := make([]Prediction, 0, len(teams))
	for _, t := range teams {
		prob := float64(titleCounts[t.ID]) / float64(p.runs) * 100.0
		out = append(out, Prediction{TeamName: t.Name, WinProbability: round2(prob)})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].WinProbability > out[j].WinProbability
	})
	return out
}

// championOf returns the team ID at the top of the table for one simulation.
// Ties are broken by Points -> GD -> GF, matching the official table.
func championOf(stats map[int]*Stats) int {
	type row struct{ id, pts, gd, gf int }
	rows := make([]row, 0, len(stats))
	for id, s := range stats {
		rows = append(rows, row{id, s.Points, s.GoalsFor - s.GoalsAgainst, s.GoalsFor})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].pts != rows[j].pts {
			return rows[i].pts > rows[j].pts
		}
		if rows[i].gd != rows[j].gd {
			return rows[i].gd > rows[j].gd
		}
		return rows[i].gf > rows[j].gf
	})
	return rows[0].id
}

// finalPredictions assigns 100% (split equally if tied on Points + GD) once
// every match has been played — there's nothing left to simulate.
func finalPredictions(repo LeagueRepository) []Prediction {
	table := computeTable(repo)
	topPts, topGD := table[0].Points, table[0].GoalDifference
	winners := 0
	for _, e := range table {
		if e.Points == topPts && e.GoalDifference == topGD {
			winners++
		}
	}
	out := make([]Prediction, 0, len(table))
	for _, e := range table {
		prob := 0.0
		if e.Points == topPts && e.GoalDifference == topGD {
			prob = 100.0 / float64(winners)
		}
		out = append(out, Prediction{TeamName: e.Name, WinProbability: round2(prob)})
	}
	return out
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// ============================================================
// SIMULATION ENGINE — wires it all together
// ============================================================

type SimulationEngine struct {
	mu        sync.Mutex // serialises PlayWeek / PlayAll
	Repo      LeagueRepository
	Simulator Simulator
	Predictor PredictionService
}

func NewSimulationEngine(repo LeagueRepository, sim Simulator, p PredictionService) *SimulationEngine {
	return &SimulationEngine{Repo: repo, Simulator: sim, Predictor: p}
}

// PlayWeek simulates every fixture in the current week and advances the clock.
func (e *SimulationEngine) PlayWeek() []MatchResult {
	e.mu.Lock()
	defer e.mu.Unlock()

	week := e.Repo.CurrentWeek()
	if week > e.Repo.TotalWeeks() {
		return nil
	}
	matches := e.Repo.GetMatchesByWeek(week)
	results := make([]MatchResult, 0, len(matches))
	for _, m := range matches {
		if m.Played {
			continue
		}
		home, _ := e.Repo.GetTeamByID(m.HomeTeamID)
		away, _ := e.Repo.GetTeamByID(m.AwayTeamID)
		hg, ag := e.Simulator.Simulate(home, away)
		m.HomeGoals, m.AwayGoals, m.Played = hg, ag, true
		e.Repo.UpdateMatch(m)
		results = append(results, MatchResult{
			Week: week, HomeTeam: home.Name, AwayTeam: away.Name,
			HomeGoals: hg, AwayGoals: ag,
		})
	}
	e.Repo.AdvanceWeek()
	return results
}

// PlayAll runs every remaining week in order.
func (e *SimulationEngine) PlayAll() []MatchResult {
	all := []MatchResult{}
	for e.Repo.CurrentWeek() <= e.Repo.TotalWeeks() {
		all = append(all, e.PlayWeek()...)
	}
	return all
}

// ============================================================
// HTTP LAYER
// ============================================================

type API struct {
	mu     sync.Mutex // protects engine pointer swap during /reset
	Engine *SimulationEngine
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (a *API) engine() *SimulationEngine {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Engine
}

func (a *API) handleLeagueTable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	eng := a.engine()
	writeJSON(w, http.StatusOK, map[string]any{
		"current_week": eng.Repo.CurrentWeek(),
		"total_weeks":  eng.Repo.TotalWeeks(),
		"table":        computeTable(eng.Repo),
	})
}

func (a *API) handleNextWeek(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	eng := a.engine()
	if eng.Repo.CurrentWeek() > eng.Repo.TotalWeeks() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "season already finished"})
		return
	}
	playedWeek := eng.Repo.CurrentWeek()
	results := eng.PlayWeek()

	resp := map[string]any{
		"week":    playedWeek,
		"results": results,
		"table":   computeTable(eng.Repo),
	}
	// Predictions kick in from week 4 onwards (per spec).
	if playedWeek >= 4 {
		resp["predictions"] = eng.Predictor.Predict()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) handlePlayAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	eng := a.engine()
	results := eng.PlayAll()
	writeJSON(w, http.StatusOK, map[string]any{
		"results":     results,
		"table":       computeTable(eng.Repo),
		"predictions": eng.Predictor.Predict(),
	})
}

func (a *API) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	a.mu.Lock()
	a.Engine = buildEngine()
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "season reset"})
}

// ============================================================
// BOOTSTRAP
// ============================================================

func defaultTeams() []Team {
	return []Team{
		{ID: 1, Name: "Chelsea", Strength: 82},
		{ID: 2, Name: "Arsenal", Strength: 85},
		{ID: 3, Name: "Manchester City", Strength: 92},
		{ID: 4, Name: "Liverpool", Strength: 88},
	}
}

func buildEngine() *SimulationEngine {
	repo := NewInMemoryRepository(defaultTeams())
	sim := NewPoissonSimulator(time.Now().UnixNano())
	pred := NewMonteCarloPredictor(repo, sim, 2000)
	return NewSimulationEngine(repo, sim, pred)
}

func main() {
	api := &API{Engine: buildEngine()}

	mux := http.NewServeMux()
	mux.HandleFunc("/league-table", api.handleLeagueTable)
	mux.HandleFunc("/next-week", api.handleNextWeek)
	mux.HandleFunc("/play-all", api.handlePlayAll)
	mux.HandleFunc("/reset", api.handleReset)

	const addr = ":8080"
	fmt.Printf("Premier League Simulation API listening on %s\n", addr)
	fmt.Println("  GET  /league-table")
	fmt.Println("  POST /next-week")
	fmt.Println("  POST /play-all")
	fmt.Println("  POST /reset")
	log.Fatal(http.ListenAndServe(addr, mux))
}
