// Package rundb provides a SQLite-backed store for kilroy run operational state.
// Every run, node execution, outcome, and edge decision is recorded and queryable.
package rundb

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps a SQLite database for run state storage.
type DB struct {
	db   *sql.DB
	path string
}

// DefaultPath returns the default global database path.
func DefaultPath() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, _ := os.UserHomeDir()
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "kilroy", "runs.db")
}

// Open opens (or creates) the run database at the given path and applies
// any pending migrations. Uses WAL mode for concurrent reads and a 5-second
// busy timeout so concurrent writers retry instead of failing immediately.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)&_pragma=synchronous(normal)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)

	rdb := &DB{db: db, path: path}
	if err := rdb.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return rdb, nil
}

// Close closes the database connection.
func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// SQL returns the underlying *sql.DB for advanced queries.
func (d *DB) SQL() *sql.DB {
	return d.db
}

// migrate applies numbered SQL migration files from the embedded filesystem.
func (d *DB) migrate() error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
	)`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Sort by filename to ensure order.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version := 0
		if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil || version == 0 {
			continue
		}

		var applied int
		row := d.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version)
		if err := row.Scan(&applied); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if applied > 0 {
			continue
		}

		content, err := migrationsFS.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			return fmt.Errorf("read migration %d: %w", version, err)
		}
		if _, err := d.db.Exec(string(content)); err != nil {
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := d.db.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
			return fmt.Errorf("record migration %d: %w", version, err)
		}
	}
	return nil
}
