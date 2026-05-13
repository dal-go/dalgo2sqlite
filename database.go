// Package dalgo2sqlite is the SQLite-specific DALgo driver.
//
// It composes [github.com/dal-go/dalgo2sql] for the [dal.DB] read/write
// surface (transactions, recordset reader, Get/Set/Insert/Delete) and
// adds SQLite-native implementations of:
//
//   - [dbschema.SchemaReader] for schema introspection via sqlite_master
//     and PRAGMA queries
//   - [ddl.SchemaModifier] for SQLite-flavored CREATE / DROP / ALTER
//   - [ddl.TransactionalDDL] (always true — SQLite supports
//     transactional DDL)
//   - [dal.ConcurrencyAware] returning false (SQLite serializes writers)
package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"

	_ "github.com/mattn/go-sqlite3" // register the "sqlite3" driver
)

// Database is the dalgo2sqlite driver instance. It implements
// [dal.DB] by delegating to an inner [dal.DB] obtained from
// [dalgo2sql.NewDatabase], and adds SQLite-specific dbschema, ddl,
// and concurrency surfaces.
//
// Construct via [NewDatabase]. Database values are safe for
// concurrent use only insofar as SQLite itself is — readers can be
// concurrent under WAL mode; writers serialize.
type Database struct {
	dal.NoConcurrency // SupportsConcurrentConnections() = false

	innerDB dal.DB  // delegate for the dal.DB surface
	sqlDB   *sql.DB // direct handle for DDL + PRAGMA queries
	dbPath  string  // remembered for diagnostics
}

// NewDatabase opens (or creates) the SQLite file at dbPath using
// github.com/mattn/go-sqlite3, pings to surface malformed-file errors
// at construction time, wraps the *sql.DB via dalgo2sql.NewDatabase
// for the dal.DB surface, and returns a *Database that satisfies
// dal.DB + dal.ConcurrencyAware.
func NewDatabase(dbPath string) (*Database, error) {
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: sql.Open(%q): %w", dbPath, err)
	}
	if pingErr := sqlDB.PingContext(context.Background()); pingErr != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("dalgo2sqlite: PingContext(%q): %w", dbPath, pingErr)
	}
	innerDB := dalgo2sql.NewDatabase(sqlDB, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{})
	return &Database{
		innerDB: innerDB,
		sqlDB:   sqlDB,
		dbPath:  dbPath,
	}, nil
}

// Close closes the underlying *sql.DB. After Close the Database value
// is unusable; further method calls will fail with an error from
// database/sql.
func (d *Database) Close() error {
	if d.sqlDB == nil {
		return nil
	}
	return d.sqlDB.Close()
}

// ID returns the driver-issued database ID (delegated to dalgo2sql).
func (d *Database) ID() string { return d.innerDB.ID() }

// Adapter returns the driver/version identifier.
func (d *Database) Adapter() dal.Adapter {
	return dal.NewAdapter("dalgo2sqlite", Version)
}

// Schema returns the dal-level Schema (delegated to dalgo2sql).
func (d *Database) Schema() dal.Schema { return d.innerDB.Schema() }

// Version is the dalgo2sqlite package version. Updated by hand on
// each release; consumed by Adapter.Version().
const Version = "0.1.0"
