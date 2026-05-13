package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dbschema"
)

// sqliteTypeFor returns the SQLite native column-type keyword that
// the ddl layer emits for the given dbschema.Type.
func sqliteTypeFor(t dbschema.Type) (string, error) {
	switch t {
	case dbschema.Bool:
		return "INTEGER", nil
	case dbschema.Int:
		return "INTEGER", nil
	case dbschema.Float:
		return "REAL", nil
	case dbschema.String:
		return "TEXT", nil
	case dbschema.Bytes:
		return "BLOB", nil
	case dbschema.Time:
		return "TEXT", nil
	case dbschema.Decimal:
		return "NUMERIC", nil
	case dbschema.Null:
		return "", fmt.Errorf("dalgo2sqlite: dbschema.Null is not a valid column type")
	default:
		return "", fmt.Errorf("dalgo2sqlite: unknown dbschema.Type %v", t)
	}
}

// dbschemaTypeFromSQLite reverses sqliteTypeFor for introspection.
// SQLite type-affinity rules per https://www.sqlite.org/datatype3.html:
//   - Contains "INT" → INTEGER → dbschema.Int
//   - Contains "CHAR"/"CLOB"/"TEXT" → TEXT → dbschema.String
//   - Contains "BLOB" → BLOB → dbschema.Bytes
//   - Contains "REAL"/"FLOA"/"DOUB" → REAL → dbschema.Float
//   - "NUMERIC"/"DECIMAL" → dbschema.Decimal
//   - Else → (Null, false) for explicit "unrecognized"
//
// Time recognition requires consulting the sidecar Time-markers table
// — this function does NOT promote TEXT to Time on its own.
func dbschemaTypeFromSQLite(declared string) (dbschema.Type, bool) {
	upper := strings.ToUpper(declared)
	switch {
	case strings.Contains(upper, "INT"):
		return dbschema.Int, true
	case strings.Contains(upper, "CHAR"),
		strings.Contains(upper, "CLOB"),
		strings.Contains(upper, "TEXT"):
		return dbschema.String, true
	case strings.Contains(upper, "BLOB"):
		return dbschema.Bytes, true
	case strings.Contains(upper, "REAL"),
		strings.Contains(upper, "FLOA"),
		strings.Contains(upper, "DOUB"):
		return dbschema.Float, true
	case upper == "NUMERIC", upper == "DECIMAL":
		return dbschema.Decimal, true
	default:
		return dbschema.Null, false
	}
}

// timeMarkerTable is the sidecar table dalgo2sqlite writes to remember
// which TEXT columns are semantically dbschema.Time.
const timeMarkerTable = "_dalgo_time_columns"

// ensureTimeMarkerTable creates the sidecar table if it does not exist.
func ensureTimeMarkerTable(ctx context.Context, db *sql.DB) error {
	const stmt = `CREATE TABLE IF NOT EXISTS ` + timeMarkerTable + ` (
		collection_name TEXT NOT NULL,
		column_name TEXT NOT NULL,
		PRIMARY KEY (collection_name, column_name)
	)`
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("dalgo2sqlite: ensureTimeMarkerTable: %w", err)
	}
	return nil
}

// writeTimeMarker inserts one Time-column marker. Idempotent via INSERT OR IGNORE.
func writeTimeMarker(ctx context.Context, db *sql.DB, collection, column string) error {
	const stmt = `INSERT OR IGNORE INTO ` + timeMarkerTable +
		` (collection_name, column_name) VALUES (?, ?)`
	if _, err := db.ExecContext(ctx, stmt, collection, column); err != nil {
		return fmt.Errorf("dalgo2sqlite: writeTimeMarker(%s.%s): %w", collection, column, err)
	}
	return nil
}

// readTimeMarkers returns the set of Time-marked column names for a collection.
// Tolerates the sidecar table being absent.
func readTimeMarkers(ctx context.Context, db *sql.DB, collection string) (map[string]bool, error) {
	out := make(map[string]bool)
	var exists string
	probeErr := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
		timeMarkerTable,
	).Scan(&exists)
	if probeErr == sql.ErrNoRows {
		return out, nil
	}
	if probeErr != nil {
		return nil, fmt.Errorf("dalgo2sqlite: readTimeMarkers probe: %w", probeErr)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT column_name FROM `+timeMarkerTable+` WHERE collection_name=?`,
		collection,
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: readTimeMarkers query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var col string
		if scanErr := rows.Scan(&col); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: readTimeMarkers scan: %w", scanErr)
		}
		out[col] = true
	}
	return out, rows.Err()
}

// dropTimeMarkers removes all markers for a collection.
func dropTimeMarkers(ctx context.Context, db *sql.DB, collection string) error {
	var exists string
	probeErr := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
		timeMarkerTable,
	).Scan(&exists)
	if probeErr == sql.ErrNoRows {
		return nil
	}
	if probeErr != nil {
		return fmt.Errorf("dalgo2sqlite: dropTimeMarkers probe: %w", probeErr)
	}
	_, err := db.ExecContext(ctx,
		`DELETE FROM `+timeMarkerTable+` WHERE collection_name=?`,
		collection,
	)
	if err != nil {
		return fmt.Errorf("dalgo2sqlite: dropTimeMarkers(%s): %w", collection, err)
	}
	return nil
}
