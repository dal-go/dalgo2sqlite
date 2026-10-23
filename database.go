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

	_ "modernc.org/sqlite" // register the "sqlite" driver (pure Go, CGO_ENABLED=0)
)

// Database is the dalgo2sqlite driver instance. It implements [dal.DB] by
// embedding a [dal.DB] obtained from [dalgo2sql.NewDatabase], and adds
// SQLite-specific dbschema, ddl, and concurrency surfaces.
//
// The embedded dal.DB (rather than a named field) is what lets Database
// satisfy dal.DB itself: dal.DB is sealed by an unexported marker method,
// and embedding is the only way for that method to be promoted onto a
// decorating type — see dal.NewDB's doc comment.
//
// Construct via [NewDatabase]. Database values are safe for
// concurrent use only insofar as SQLite itself is — readers can be
// concurrent under WAL mode; writers serialize.
type Database struct {
	dal.NoConcurrency // SupportsConcurrentConnections() = false

	dal.DB         // delegate for the dal.DB surface
	sqlDB  *sql.DB // direct handle for DDL + PRAGMA queries
	dbPath string  // remembered for diagnostics

	// lenient is whether the schema reads describe the file the way SQLite
	// lists it rather than refuse what a lossless export cannot carry (see
	// LenientSchema).
	lenient bool
}

// LenientSchema returns a Database over the same connection whose schema reads
// describe a file the way SQLite itself lists it, for a reader that shows a
// database to a person (a scan, a viewer) rather than copies it:
//
//   - DescribeCollection describes a view too, by the columns SQLite lists for it;
//   - a column whose declared type has no portable type (none at all, or one such
//     as JSON or UUID) is [dbschema.Null] instead of failing the description, and
//     its declared type stays in SourceDefinition;
//   - generated columns and the hidden columns of a virtual table are left out of
//     the description, as PRAGMA table_info leaves them out, instead of failing it;
//   - a table or a view with an empty name is left out of ListCollections and
//     ListViews (UnnamedObjects lists them), where the strict reader refuses it;
//   - an index on a column the description leaves out is left out of Indexes and
//     ListIndexes; SourceDefinition.Indexes keeps it.
//
// The Database it is called on is unchanged: its schema reads still refuse what a
// lossless export cannot represent. The two share one connection pool, so closing
// either closes both: close the Database that owns the pool, not the lenient one.
func (d *Database) LenientSchema() *Database {
	lenient := *d
	lenient.lenient = true
	return &lenient
}

// NewDatabase opens (or creates) the SQLite file at dbPath using
// modernc.org/sqlite (pure Go, CGO_ENABLED=0), pings to surface
// malformed-file errors at construction time, wraps the *sql.DB via
// dalgo2sql.NewDatabase for the dal.DB surface, and returns a *Database
// that satisfies dal.DB + dal.ConcurrencyAware.
//
// Use [NewDatabaseWithOptions] when you need to supply per-collection
// primary-key metadata (required for Insert/Get/Delete with map[string]any data).
func NewDatabase(dbPath string) (*Database, error) {
	return NewDatabaseWithOptions(dbPath, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{})
}

// NewDatabaseWithOptions is like [NewDatabase] but accepts a dal.Schema and
// dalgo2sql.DbOptions so callers can configure per-collection primary-key
// mappings required by Insert/Get/Delete operations.
//
// If opts.IsAlreadyExists is nil, it defaults to this package's
// [IsAlreadyExists], so an Insert over an existing primary key or unique
// index fails with an error satisfying record.IsAlreadyExists without any
// extra configuration. Set opts.IsAlreadyExists explicitly to override that
// default (e.g. with a func that also calls dalgo2sqlite.IsAlreadyExists).
//
// Example — open a DB whose "widgets" table has "id" as its primary key:
//
//	db, err := dalgo2sqlite.NewDatabaseWithOptions(path, dal.NewSchema(nil, nil),
//	    dalgo2sql.DbOptions{
//	        Recordsets: map[string]*dalgo2sql.Recordset{
//	            "widgets": dalgo2sql.NewRecordset("widgets", dalgo2sql.Table,
//	                []dal.FieldRef{dal.Field("id")}),
//	        },
//	    })
var sqlOpen = func(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", dbPath)
}

func NewDatabaseWithOptions(dbPath string, schema dal.Schema, opts dalgo2sql.DbOptions) (*Database, error) {
	if opts.IsAlreadyExists == nil {
		opts.IsAlreadyExists = IsAlreadyExists
	}
	sqlDB, err := sqlOpen(dbPath)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: sql.Open(%q): %w", dbPath, err)
	}
	if pingErr := sqlDB.PingContext(context.Background()); pingErr != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("dalgo2sqlite: PingContext(%q): %w", dbPath, pingErr)
	}
	innerDB := dalgo2sql.NewDatabase(sqlDB, schema, opts)
	return &Database{
		DB:     innerDB,
		sqlDB:  sqlDB,
		dbPath: dbPath,
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

// SupportsConcurrentConnections reports SQLite's own concurrency behaviour
// (always false — see dal.NoConcurrency), not dalgo2sql's. An explicit
// method is required here: dal.NoConcurrency and the embedded dal.DB (whose
// Backend requirement embeds dal.ConcurrencyAware) both declare this method
// at the same promotion depth, which Go otherwise treats as an ambiguous
// selector.
func (d *Database) SupportsConcurrentConnections() bool {
	return d.NoConcurrency.SupportsConcurrentConnections()
}

// ID returns the driver-issued database ID (delegated to dalgo2sql).
func (d *Database) ID() string { return d.DB.ID() }

// Adapter returns the driver/version identifier.
func (d *Database) Adapter() dal.Adapter {
	return dal.NewAdapter("dalgo2sqlite", Version)
}

// Schema returns the dal-level Schema (delegated to dalgo2sql).
func (d *Database) Schema() dal.Schema { return d.DB.Schema() }

// Version is the dalgo2sqlite package version. Updated by hand on
// each release; consumed by Adapter.Version().
const Version = "0.1.0"
