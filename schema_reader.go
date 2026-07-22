package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
)

// ListCollections returns the user-defined tables in alphabetical
// order. The parent *record.Key is ignored — SQLite has no
// catalog/schema hierarchy.
func (d *Database) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	_ = parent // ignored
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT name FROM sqlite_master
		 WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != ?
		 ORDER BY name`,
		timeMarkerTable,
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: ListCollections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []dal.CollectionRef
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: ListCollections scan: %w", scanErr)
		}
		out = append(out, dal.NewRootCollectionRef(name, ""))
	}
	return out, rows.Err()
}

// DescribeCollection is implemented in T15.
func (d *Database) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return describeCollectionImpl(ctx, d.sqlDB, ref.Name())
}

// ListIndexes is implemented in T16.
func (d *Database) ListIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return listIndexesImpl(ctx, d.sqlDB, ref.Name())
}

// ---- DescribeCollection impl (T15) ----

type pkEntry struct {
	colName string
	pkOrder int
}

// describeCollectionImpl is the inner reader, factored so AlterCollection
// can also use it from within a transaction (variant added later).
func describeCollectionImpl(ctx context.Context, db *sql.DB, name string) (*dbschema.CollectionDef, error) {
	// 1. Confirm the table exists.
	var found string
	probeErr := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
		name,
	).Scan(&found)
	if probeErr == sql.ErrNoRows {
		return nil, newCollectionNotFoundError(name)
	}
	if probeErr != nil {
		return nil, fmt.Errorf("dalgo2sqlite: DescribeCollection probe %q: %w", name, probeErr)
	}

	// 2. pragma_table_info.
	rows, err := db.QueryContext(ctx,
		`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`,
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: DescribeCollection pragma %q: %w", name, err)
	}
	defer func() { _ = rows.Close() }()

	// 3. Time-column markers.
	timeMarkers, tmErr := readTimeMarkers(ctx, db, name)
	if tmErr != nil {
		return nil, tmErr
	}

	var fields []dbschema.FieldDef
	var pkEntries []pkEntry

	for rows.Next() {
		var (
			colName    string
			declType   string
			notnull    int
			dfltValue  sql.NullString
			pkPosition int
		)
		if scanErr := rows.Scan(&colName, &declType, &notnull, &dfltValue, &pkPosition); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: pragma_table_info scan: %w", scanErr)
		}
		_ = dfltValue // Default population is plan-deferred

		var t dbschema.Type
		var precision *dbschema.Precision
		if timeMarkers[colName] {
			t = dbschema.Time
		} else {
			var ok bool
			t, precision, ok = dbschemaTypeFromSQLite(declType)
			if !ok {
				return nil, &dbschema.NotSupportedError{
					Op:      "DescribeCollection",
					Backend: "dalgo2sqlite",
					Reason:  fmt.Sprintf("column %q has unrecognized SQLite type %q", colName, declType),
				}
			}
		}
		// PK columns are implicitly NOT NULL in SQLite even when notnull=0.
		nullable := notnull == 0 && pkPosition == 0
		f := dbschema.FieldDef{
			Name:      dal.FieldName(colName),
			Type:      t,
			Precision: precision,
			Nullable:  nullable,
		}
		if t == dbschema.Int && pkPosition == 1 {
			f.AutoIncrement, _ = tableHasAutoIncrement(ctx, db, name)
		}
		fields = append(fields, f)
		if pkPosition > 0 {
			pkEntries = append(pkEntries, pkEntry{colName: colName, pkOrder: pkPosition})
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("dalgo2sqlite: pragma_table_info rows: %w", rowsErr)
	}

	sortPKByOrder(pkEntries)
	pk := make([]dal.FieldName, len(pkEntries))
	for i, e := range pkEntries {
		pk[i] = dal.FieldName(e.colName)
	}

	indexes, err := listIndexesImpl(ctx, db, name)
	if err != nil {
		return nil, err
	}

	return &dbschema.CollectionDef{
		Name:       name,
		Fields:     fields,
		PrimaryKey: pk,
		Indexes:    indexes,
	}, nil
}

func tableHasAutoIncrement(ctx context.Context, db *sql.DB, table string) (bool, error) {
	// sqlite_sequence only gains a row after the first INSERT, so we
	// detect AUTOINCREMENT by inspecting the CREATE TABLE DDL directly.
	var ddl string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table,
	).Scan(&ddl)
	if err != nil {
		return false, fmt.Errorf("dalgo2sqlite: tableHasAutoIncrement DDL query: %w", err)
	}
	return strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT"), nil
}

func sortPKByOrder(entries []pkEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j-1].pkOrder > entries[j].pkOrder; j-- {
			entries[j-1], entries[j] = entries[j], entries[j-1]
		}
	}
}

// ---- ListIndexes impl (T16) ----

func listIndexesImpl(ctx context.Context, db *sql.DB, name string) ([]dbschema.IndexDef, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, "unique", origin FROM pragma_index_list(?)`,
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: pragma_index_list %q: %w", name, err)
	}
	defer func() { _ = rows.Close() }()

	var out []dbschema.IndexDef
	for rows.Next() {
		var (
			ixName string
			unique int
			origin string
		)
		if scanErr := rows.Scan(&ixName, &unique, &origin); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: pragma_index_list scan: %w", scanErr)
		}
		if origin == "pk" {
			continue
		}
		fields, fErr := readIndexFields(ctx, db, ixName)
		if fErr != nil {
			return nil, fErr
		}
		out = append(out, dbschema.IndexDef{
			Name:       ixName,
			Collection: name,
			Fields:     fields,
			Unique:     unique != 0,
		})
	}
	return out, rows.Err()
}

func readIndexFields(ctx context.Context, db *sql.DB, indexName string) ([]dal.FieldName, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM pragma_index_info(?) ORDER BY seqno`,
		indexName,
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: pragma_index_info %q: %w", indexName, err)
	}
	defer func() { _ = rows.Close() }()
	var out []dal.FieldName
	for rows.Next() {
		var col string
		if scanErr := rows.Scan(&col); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: pragma_index_info scan: %w", scanErr)
		}
		out = append(out, dal.FieldName(col))
	}
	return out, rows.Err()
}
