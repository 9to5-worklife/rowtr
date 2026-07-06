// Package usage persists every routed request so Rowtr can show how much was
// saved by serving locally and which models handled traffic. It uses a pure-Go
// SQLite driver (modernc.org/sqlite) so the CLI/proxy binary stays cgo-free.
package usage

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Event is one routed request.
type Event struct {
	Time         time.Time
	Tier         string // "local" | "frontier"
	Model        string
	InputTokens  int
	OutputTokens int
	LatencyMS    int64
	Offloaded    bool
	CostUSD      float64 // actual cost (0 for local)
	SavedUSD     float64 // estimated frontier cost avoided (0 for frontier)
}

// Store is the append-and-aggregate usage database.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the usage database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// WAL + a busy timeout let the proxy write while the tray reads.
	db.Exec(`PRAGMA busy_timeout=5000;`)
	db.Exec(`PRAGMA journal_mode=WAL;`)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS events (
		ts         INTEGER,
		tier       TEXT,
		model      TEXT,
		in_tokens  INTEGER,
		out_tokens INTEGER,
		latency_ms INTEGER,
		offloaded  INTEGER,
		cost_usd   REAL,
		saved_usd  REAL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Record inserts one event.
func (s *Store) Record(e Event) error {
	off := 0
	if e.Offloaded {
		off = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO events (ts,tier,model,in_tokens,out_tokens,latency_ms,offloaded,cost_usd,saved_usd)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		e.Time.Unix(), e.Tier, e.Model, e.InputTokens, e.OutputTokens, e.LatencyMS, off, e.CostUSD, e.SavedUSD,
	)
	return err
}

// ModelStat is per-(model,tier) aggregate.
type ModelStat struct {
	Model     string
	Tier      string
	Count     int
	InTokens  int
	OutTokens int
	CostUSD   float64
	SavedUSD  float64
}

// Summary is the whole-history rollup the tray app displays.
type Summary struct {
	Total          int
	Local          int
	Frontier       int
	LocalTokens    int     // tokens served locally = tokens kept off the Claude quota
	FrontierTokens int     // tokens actually spent on Claude (from response usage)
	SavedUSD       float64 // estimated $ avoided (API pricing only)
	SpentUSD       float64 // estimated frontier spend (API pricing only)
	ByModel        []ModelStat
}

// Summary aggregates all recorded events.
func (s *Store) Summary() (Summary, error) {
	var sum Summary
	row := s.db.QueryRow(`SELECT
		count(*),
		coalesce(sum(tier = 'local'), 0),
		coalesce(sum(tier = 'frontier'), 0),
		coalesce(sum(CASE WHEN tier = 'local' THEN in_tokens + out_tokens ELSE 0 END), 0),
		coalesce(sum(CASE WHEN tier = 'frontier' THEN in_tokens + out_tokens ELSE 0 END), 0),
		coalesce(sum(saved_usd), 0),
		coalesce(sum(cost_usd), 0)
		FROM events`)
	if err := row.Scan(&sum.Total, &sum.Local, &sum.Frontier, &sum.LocalTokens, &sum.FrontierTokens, &sum.SavedUSD, &sum.SpentUSD); err != nil {
		return sum, err
	}

	rows, err := s.db.Query(`SELECT model, tier, count(*),
		coalesce(sum(in_tokens), 0), coalesce(sum(out_tokens), 0),
		coalesce(sum(cost_usd), 0), coalesce(sum(saved_usd), 0)
		FROM events GROUP BY model, tier ORDER BY count(*) DESC`)
	if err != nil {
		return sum, err
	}
	defer rows.Close()
	for rows.Next() {
		var m ModelStat
		if err := rows.Scan(&m.Model, &m.Tier, &m.Count, &m.InTokens, &m.OutTokens, &m.CostUSD, &m.SavedUSD); err != nil {
			return sum, err
		}
		sum.ByModel = append(sum.ByModel, m)
	}
	return sum, rows.Err()
}
