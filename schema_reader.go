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
		 WHERE type='table' AND `+ownTablesFilter+` AND name != ?
		 ORDER BY name`,
		timeMarkerTable,
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: ListCollections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return d.collectionRefs(rows, "ListCollections", "table")
}

// collectionRefs reads the names of rows into references. SQLite allows a table or
// a view with an empty name, which no reference can hold: the strict reader
// refuses the file, as a lossless export cannot carry it, and the lenient one
// (see LenientSchema) leaves it out; UnnamedObjects says what it left out.
func (d *Database) collectionRefs(rows *sql.Rows, op, kind string) ([]dal.CollectionRef, error) {
	var out []dal.CollectionRef
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: %s scan: %w", op, scanErr)
		}
		switch {
		case name != "":
			out = append(out, dal.NewRootCollectionRef(name, ""))
		case !d.lenient:
			return nil, &dbschema.NotSupportedError{Op: op, Backend: "dalgo2sqlite",
				Reason: "a " + kind + " with an empty name, unsupported for lossless export"}
		}
	}
	return out, rows.Err()
}

// UnnamedObjects lists the kind ("table" or "view") of each table and view of the
// database whose name is empty, which SQLite allows and no reference can hold: the
// lenient reader leaves them out of its listings, and its caller can say so.
func (d *Database) UnnamedObjects(ctx context.Context) ([]string, error) {
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT type FROM sqlite_master WHERE type IN ('table', 'view') AND name = '' ORDER BY type`)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: UnnamedObjects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var kinds []string
	for rows.Next() {
		var kind string
		if scanErr := rows.Scan(&kind); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: UnnamedObjects scan: %w", scanErr)
		}
		kinds = append(kinds, kind)
	}
	return kinds, rows.Err()
}

// ownTablesFilter keeps SQLite's own tables (sqlite_sequence, sqlite_stat1 and
// every other name SQLite reserves with the "sqlite_" prefix) out of a listing.
// The underscore is escaped: unescaped, it matches any character, and a user's
// table named, say, "sqliteish" would be left out.
const ownTablesFilter = `name NOT LIKE 'sqlite\_%' ESCAPE '\'`

// ListViews returns the views of the database in alphabetical order; a view
// with an empty name is treated as ListCollections treats a table with one. A view is not
// among the collections ListCollections returns; on a Database made by
// LenientSchema, DescribeCollection describes one.
func (d *Database) ListViews(ctx context.Context) ([]dal.CollectionRef, error) {
	rows, err := d.sqlDB.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='view' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: ListViews: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return d.collectionRefs(rows, "ListViews", "view")
}

// DescribeCollection is implemented in T15.
func (d *Database) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return describeCollection(ctx, d.sqlDB, ref.Name(), d.lenient)
}

// ListIndexes is implemented in T16.
func (d *Database) ListIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	indexes, err := listIndexesImpl(ctx, d.sqlDB, ref.Name())
	if err != nil || !d.lenient {
		return indexes, err
	}
	// Columns the lenient description leaves out cannot be named by its indexes.
	def, err := describeCollection(ctx, d.sqlDB, ref.Name(), true)
	if err != nil {
		return nil, err
	}
	return indexesOnFields(indexes, def.Fields), nil
}

// ---- DescribeCollection impl (T15) ----

type pkEntry struct {
	colName string
	pkOrder int
}

// describeCollection is the inner reader: it describes the table name, or with
// lenient the table or view (see Database.LenientSchema).
func describeCollection(ctx context.Context, db *sql.DB, name string, lenient bool) (*dbschema.CollectionDef, error) {
	// 1. Confirm the table exists.
	kinds := `type='table'`
	if lenient {
		kinds = `type IN ('table', 'view')`
	}
	var found string
	probeErr := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE `+kinds+` AND name=?`,
		name,
	).Scan(&found)
	if probeErr == sql.ErrNoRows {
		return nil, newCollectionNotFoundError(name)
	}
	if probeErr != nil {
		return nil, fmt.Errorf("dalgo2sqlite: DescribeCollection probe %q: %w", name, probeErr)
	}

	// 2. Time-column markers. This is an independent query and runs before the
	// pragma_table_info rows below: a pool limited to one open connection
	// cannot serve a query while those rows still hold that connection.
	timeMarkers, tmErr := readTimeMarkers(ctx, db, name)
	if tmErr != nil {
		return nil, tmErr
	}

	// 3. pragma_table_info.
	rows, err := db.QueryContext(ctx,
		`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`,
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: DescribeCollection pragma %q: %w", name, err)
	}
	defer func() { _ = rows.Close() }()

	var fields []dbschema.FieldDef
	var pkEntries []pkEntry
	// The AUTOINCREMENT probe is a query of its own: remember the field that
	// needs it and ask once the rows above are drained and closed.
	autoIncrementField := -1

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
		// The portable DefaultExpr set cannot represent arbitrary SQLite
		// expressions. The raw expression is retained in SourceDefinition.

		var t dbschema.Type
		var precision *dbschema.Precision
		if timeMarkers[colName] {
			t = dbschema.Time
		} else {
			var ok bool
			t, precision, ok = dbschemaTypeFromSQLite(declType)
			if !ok && !lenient {
				return nil, &dbschema.NotSupportedError{
					Op:      "DescribeCollection",
					Backend: "dalgo2sqlite",
					Reason:  fmt.Sprintf("column %q has unrecognized SQLite type %q", colName, declType),
				}
			}
		}
		// A non-INTEGER PRIMARY KEY can contain NULL in an ordinary SQLite
		// rowid table when NOT NULL was not declared. The source catalog flag
		// remains available separately even when a provider applies stricter
		// effective PK semantics.
		nullable := notnull == 0
		f := dbschema.FieldDef{
			Name:      dal.FieldName(colName),
			Type:      t,
			Precision: precision,
			Nullable:  nullable,
		}
		if t == dbschema.Int && pkPosition == 1 {
			autoIncrementField = len(fields)
		}
		fields = append(fields, f)
		if pkPosition > 0 {
			pkEntries = append(pkEntries, pkEntry{colName: colName, pkOrder: pkPosition})
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("dalgo2sqlite: pragma_table_info rows: %w", rowsErr)
	}
	_ = rows.Close()
	if autoIncrementField >= 0 {
		fields[autoIncrementField].AutoIncrement, _ = tableHasAutoIncrement(ctx, db, name)
	}

	sortPKByOrder(pkEntries)
	pk := make([]dal.FieldName, len(pkEntries))
	for i, e := range pkEntries {
		pk[i] = dal.FieldName(e.colName)
	}

	indexes, err := listIndexesImpl(ctx, db, name)
	if err == nil && lenient {
		indexes = indexesOnFields(indexes, fields)
	}
	if err != nil {
		return nil, err
	}
	foreignKeys, err := readForeignKeys(ctx, db, name)
	if err != nil {
		return nil, err
	}
	sourceDefinition, err := readSourceDefinition(ctx, db, name, lenient)
	if err != nil {
		return nil, err
	}
	hasPKIndex := false
	for _, index := range sourceDefinition.Indexes {
		if index.Origin == "pk" {
			hasPKIndex = true
			break
		}
	}
	for i, col := range sourceDefinition.Columns {
		if i >= len(fields) || col.Name != string(fields[i].Name) {
			return nil, fmt.Errorf("dalgo2sqlite: source columns differ from portable fields in %q", name)
		}
		// A sole INTEGER PRIMARY KEY is a rowid alias and cannot remain NULL.
		if len(pk) == 1 && col.PrimaryKeyPosition == 1 && strings.EqualFold(strings.TrimSpace(col.DeclaredType), "INTEGER") &&
			!hasPKIndex && !strings.Contains(strings.ToUpper(sourceDefinition.CreateSQL), "WITHOUT ROWID") {
			fields[i].Nullable = false
		}
	}

	return &dbschema.CollectionDef{
		Name:             name,
		Fields:           fields,
		PrimaryKey:       pk,
		Indexes:          indexes,
		ForeignKeys:      foreignKeys,
		SourceDefinition: sourceDefinition,
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

// indexesOnFields is indexes without those on a column that is not among fields: a
// generated column, which the lenient description leaves out. Such an index stays
// in SourceDefinition.Indexes, which describes the catalog as it is.
func indexesOnFields(indexes []dbschema.IndexDef, fields []dbschema.FieldDef) []dbschema.IndexDef {
	described := make(map[dal.FieldName]bool, len(fields))
	for _, field := range fields {
		described[field.Name] = true
	}
	kept := indexes[:0:0]
	for _, index := range indexes {
		onDescribed := true
		for _, field := range index.Fields {
			onDescribed = onDescribed && described[field]
		}
		if onDescribed {
			kept = append(kept, index)
		}
	}
	return kept
}

func listIndexesImpl(ctx context.Context, db *sql.DB, name string) ([]dbschema.IndexDef, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, "unique", origin FROM pragma_index_list(?)`,
		name,
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: pragma_index_list %q: %w", name, err)
	}
	defer func() { _ = rows.Close() }()

	// Read the index list before querying the fields of each index: on a pool
	// limited to one open connection, a nested query waits for the rows here
	// to release it, and those rows only release it when drained.
	type listedIndex struct {
		name   string
		unique int
		origin string
	}
	var listed []listedIndex
	for rows.Next() {
		var index listedIndex
		if scanErr := rows.Scan(&index.name, &index.unique, &index.origin); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: pragma_index_list scan: %w", scanErr)
		}
		listed = append(listed, index)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("dalgo2sqlite: pragma_index_list rows: %w", rowsErr)
	}
	_ = rows.Close()

	var out []dbschema.IndexDef
	for _, index := range listed {
		if index.origin == "pk" {
			continue
		}
		fields, fErr := readIndexFields(ctx, db, index.name)
		if fErr != nil {
			return nil, fErr
		}
		if len(fields) == 0 {
			// Portable IndexDef cannot represent expression terms. The
			// complete index remains in SourceDefinition.Indexes.
			continue
		}
		out = append(out, dbschema.IndexDef{
			Name:       index.name,
			Collection: name,
			Fields:     fields,
			Unique:     index.unique != 0,
		})
	}
	return out, nil
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
	hasExpression := false
	for rows.Next() {
		var col sql.NullString
		if scanErr := rows.Scan(&col); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: pragma_index_info scan: %w", scanErr)
		}
		if !col.Valid {
			hasExpression = true
			continue
		}
		out = append(out, dal.FieldName(col.String))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if hasExpression {
		return nil, nil
	}
	return out, nil
}
