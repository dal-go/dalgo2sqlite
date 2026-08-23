package dalgo2sqlite

import (
	"errors"

	"modernc.org/sqlite"
)

// sqliteConstraintPrimaryKey and sqliteConstraintUnique are the extended
// SQLite result codes modernc.org/sqlite reports for a duplicate primary
// key or a duplicate unique-index value:
//
//	SQLITE_CONSTRAINT_PRIMARYKEY = 1555
//	SQLITE_CONSTRAINT_UNIQUE     = 2067
//
// modernc.org/sqlite turns extended result codes on for every connection it
// opens — see modernc.org/sqlite's conn.go open(), which calls
// sqlite3_extended_result_codes(db, 1) unconditionally before the
// connection is handed back — so (*sqlite.Error).Code() reports one of
// these extended codes rather than falling back to the primary
// SQLITE_CONSTRAINT (19). Matching only SQLITE_CONSTRAINT_UNIQUE would miss
// a duplicate primary key, which reports SQLITE_CONSTRAINT_PRIMARYKEY
// instead and is the commonest case (an INSERT over an existing row's
// primary key).
//
// Values are hardcoded here rather than importing the generated
// modernc.org/sqlite/lib package for two integers; both were verified
// against modernc.org/sqlite@v1.57.0's lib/sqlite.go.
const (
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

// IsAlreadyExists reports whether err — the raw error modernc.org/sqlite
// (the pure-Go SQLite driver this adapter registers; see database.go)
// returns for a failed INSERT — represents a duplicate-key violation: a
// duplicate primary key or a duplicate value in a UNIQUE index.
//
// It is dalgo2sqlite's implementation of the
// [github.com/dal-go/dalgo2sql.DbOptions.IsAlreadyExists] hook, which
// dalgo2sql itself cannot supply because detecting a duplicate key is
// driver-specific. NewDatabase and NewDatabaseWithOptions wire it in by
// default whenever the caller-supplied DbOptions leaves IsAlreadyExists
// nil (see database.go), so ordinary use needs no extra configuration.
// It is exported so a caller assembling a custom dalgo2sql.DbOptions can
// still reference it directly — e.g. to compose it with another
// classifier, or to confirm what it matches.
//
// It matches only on errors.As(err, *sqlite.Error) and that error's
// extended result code — never on message text, which is not a stable
// contract across modernc.org/sqlite versions or SQLite builds. The
// primary result code SQLITE_CONSTRAINT (19) is deliberately not matched:
// it also covers NOT NULL, CHECK, and FOREIGN KEY violations, none of
// which are duplicate keys, so matching it would misclassify them.
func IsAlreadyExists(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() {
	case sqliteConstraintPrimaryKey, sqliteConstraintUnique:
		return true
	default:
		return false
	}
}
