# Premier League Simulation API

A 6-week Premier League simulator written in idiomatic Go (stdlib only, zero dependencies).
Simulates a 4-team double round-robin, tracks the league table, and predicts championship
probabilities via Monte Carlo from week 4 onwards.

## Run

```bash
go run main.go
```

Server starts on `http://localhost:8080`.

## Endpoints

| Method | Path            | Description                                                          |
| ------ | --------------- | -------------------------------------------------------------------- |
| GET    | `/league-table` | Current standings, sorted by Points → Goal Difference → Goals For    |
| POST   | `/next-week`    | Simulates the current week. Returns results + table (+ predictions from week 4) |
| POST   | `/play-all`     | Simulates every remaining week instantly                             |
| POST   | `/reset`        | Resets the season                                                    |

## Example

```bash
curl http://localhost:8080/league-table
curl -X POST http://localhost:8080/next-week
curl -X POST http://localhost:8080/play-all
```

## Architecture

- **Domain models** (`Team`, `Stats`, `Match`, `LeagueTableEntry`) use struct composition.
- **Three interfaces** keep the engine swappable:
  - `Simulator` — Poisson model weighted by team `Strength` + home advantage
  - `LeagueRepository` — abstracts storage (in-memory here; SQL schema in `schema.sql`)
  - `PredictionService` — Monte Carlo (2,000 runs over remaining fixtures)
- **`SimulationEngine`** wires them together and serializes mutations with a mutex.

## Teams & strengths

| Team             | Strength |
| ---------------- | -------- |
| Manchester City  | 92       |
| Liverpool        | 88       |
| Arsenal          | 85       |
| Chelsea          | 82       |

## Files

- `main.go` — full server, runnable as-is
- `schema.sql` — Postgres schema + common queries mirroring the in-memory repo
