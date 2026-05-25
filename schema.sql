-- =====================================================================
-- Premier League Simulation — schema.sql
-- Mirrors the in-memory LeagueRepository. Postgres dialect; trivial to
-- port to MySQL/SQLite (swap SERIAL -> AUTO_INCREMENT / INTEGER PK).
-- =====================================================================

DROP TABLE IF EXISTS matches;
DROP TABLE IF EXISTS league_state;
DROP TABLE IF EXISTS teams;

CREATE TABLE teams (
    id        SERIAL PRIMARY KEY,
    name      VARCHAR(100) NOT NULL UNIQUE,
    strength  INTEGER      NOT NULL CHECK (strength BETWEEN 1 AND 100)
);

CREATE TABLE matches (
    id            SERIAL  PRIMARY KEY,
    week          INTEGER NOT NULL CHECK (week BETWEEN 1 AND 6),
    home_team_id  INTEGER NOT NULL REFERENCES teams(id),
    away_team_id  INTEGER NOT NULL REFERENCES teams(id),
    home_goals    INTEGER NOT NULL DEFAULT 0,
    away_goals    INTEGER NOT NULL DEFAULT 0,
    played        BOOLEAN NOT NULL DEFAULT FALSE,
    CHECK (home_team_id <> away_team_id)
);
CREATE INDEX idx_matches_week    ON matches(week);
CREATE INDEX idx_matches_played  ON matches(played);

-- Single-row table holding the season clock.
CREATE TABLE league_state (
    id            INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    current_week  INTEGER NOT NULL DEFAULT 1,
    total_weeks   INTEGER NOT NULL DEFAULT 6
);

-- ---------------------------------------------------------------------
-- Seed data
-- ---------------------------------------------------------------------
INSERT INTO teams (name, strength) VALUES
    ('Chelsea',         82),
    ('Arsenal',         85),
    ('Manchester City', 92),
    ('Liverpool',       88);

INSERT INTO league_state (id, current_week, total_weeks) VALUES (1, 1, 6);

-- Double round-robin fixture list (12 matches over 6 weeks).
-- IDs below assume the seed insert above runs first.
INSERT INTO matches (week, home_team_id, away_team_id) VALUES
    -- First leg
    (1, 1, 2), (1, 3, 4),
    (2, 1, 3), (2, 2, 4),
    (3, 1, 4), (3, 2, 3),
    -- Return leg (home/away swapped)
    (4, 2, 1), (4, 4, 3),
    (5, 3, 1), (5, 4, 2),
    (6, 4, 1), (6, 3, 2);

-- =====================================================================
-- COMMON QUERIES (parameterised with $1, $2, ...)
-- =====================================================================

-- Q1: List all teams.
-- SELECT id, name, strength FROM teams ORDER BY id;

-- Q2: Get every fixture for a given week.
-- SELECT id, week, home_team_id, away_team_id, home_goals, away_goals, played
-- FROM matches
-- WHERE week = $1
-- ORDER BY id;

-- Q3: Record a match result.
-- UPDATE matches
-- SET home_goals = $1, away_goals = $2, played = TRUE
-- WHERE id = $3;

-- Q4: Advance the season clock by one week.
-- UPDATE league_state SET current_week = current_week + 1 WHERE id = 1;

-- Q5: Pull the live league table (Points DESC, GD DESC, GF DESC).
-- SELECT
--     t.id,
--     t.name,
--     COUNT(m.id)                                                          AS played,
--     SUM(CASE WHEN (m.home_team_id = t.id AND m.home_goals > m.away_goals)
--               OR (m.away_team_id = t.id AND m.away_goals > m.home_goals)
--              THEN 1 ELSE 0 END)                                          AS won,
--     SUM(CASE WHEN m.home_goals = m.away_goals THEN 1 ELSE 0 END)         AS drawn,
--     SUM(CASE WHEN (m.home_team_id = t.id AND m.home_goals < m.away_goals)
--               OR (m.away_team_id = t.id AND m.away_goals < m.home_goals)
--              THEN 1 ELSE 0 END)                                          AS lost,
--     SUM(CASE WHEN m.home_team_id = t.id THEN m.home_goals ELSE m.away_goals END) AS goals_for,
--     SUM(CASE WHEN m.home_team_id = t.id THEN m.away_goals ELSE m.home_goals END) AS goals_against,
--     SUM(CASE WHEN m.home_team_id = t.id THEN m.home_goals ELSE m.away_goals END)
--   - SUM(CASE WHEN m.home_team_id = t.id THEN m.away_goals ELSE m.home_goals END) AS goal_difference,
--     SUM(CASE
--         WHEN (m.home_team_id = t.id AND m.home_goals > m.away_goals)
--           OR (m.away_team_id = t.id AND m.away_goals > m.home_goals) THEN 3
--         WHEN m.home_goals = m.away_goals THEN 1
--         ELSE 0
--     END)                                                                 AS points
-- FROM teams t
-- LEFT JOIN matches m
--     ON (m.home_team_id = t.id OR m.away_team_id = t.id) AND m.played = TRUE
-- GROUP BY t.id, t.name
-- ORDER BY points DESC, goal_difference DESC, goals_for DESC;

-- Q6: Fetch remaining (unplayed) fixtures — input to the prediction engine.
-- SELECT id, week, home_team_id, away_team_id
-- FROM matches
-- WHERE played = FALSE
-- ORDER BY week, id;

-- Q7: Reset the season (re-run the seed block above after this).
-- UPDATE matches SET home_goals = 0, away_goals = 0, played = FALSE;
-- UPDATE league_state SET current_week = 1 WHERE id = 1;
