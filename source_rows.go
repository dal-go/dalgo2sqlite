package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

var _ dbschema.SourceRowsReader = (*Database)(nil)

type sourceRowsCursor struct {
	rows    *sql.Rows
	names   []string
	decimal []bool
}

// OpenSourceRows streams every native table row with its SQLite storage
// classes. A declared NUMERIC/DECIMAL value stored as REAL is rendered as the
// shortest decimal string that round-trips to its stored binary64 value;
// SQLite cannot recover decimal digits that were lost on initial insertion.
func (d *Database) OpenSourceRows(ctx context.Context, ref *dal.CollectionRef) (dbschema.SourceRowCursor, error) {
	if ref == nil || ref.Name() == "" {
		return nil, fmt.Errorf("dalgo2sqlite: source collection name is empty")
	}
	table := ref.Name()
	if err := rejectHiddenSourceColumns(ctx, d.sqlDB, table); err != nil {
		return nil, err
	}
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT name, type, pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: source row columns for %q: %w", table, err)
	}
	var names []string
	var decimal []bool
	var pk []pkEntry
	for rows.Next() {
		var name, declared string
		var position int
		if err := rows.Scan(&name, &declared, &position); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("dalgo2sqlite: source row column scan for %q: %w", table, err)
		}
		names = append(names, name)
		t, _, recognized := dbschemaTypeFromSQLite(declared)
		decimal = append(decimal, recognized && t == dbschema.Decimal)
		if position > 0 {
			pk = append(pk, pkEntry{colName: name, pkOrder: position})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("dalgo2sqlite: source row column rows for %q: %w", table, err)
	}
	_ = rows.Close()
	if len(names) == 0 {
		return nil, fmt.Errorf("dalgo2sqlite: no source columns for %q", table)
	}
	sortPKByOrder(pk)
	selects := make([]string, 0, len(names)*2)
	for _, name := range names {
		quoted := quoteIdentifier(name)
		// A bare DATETIME column is decoded by some SQLite drivers as
		// time.Time even when its actual SQLite storage class is TEXT.
		// Project through an expression so the driver's declared-type
		// conversion cannot rewrite the source's lexical text value.
		selects = append(selects, "CASE WHEN typeof("+quoted+")='text' THEN CAST("+quoted+" AS TEXT) ELSE "+quoted+" END")
	}
	for _, name := range names {
		selects = append(selects, "typeof("+quoteIdentifier(name)+")")
	}
	order := ""
	if len(pk) > 0 {
		ordered := make([]string, len(pk))
		for i, key := range pk {
			ordered[i] = quoteIdentifier(key.colName)
		}
		order = strings.Join(ordered, ", ")
	} else {
		// A user column can shadow SQLite's hidden rowid aliases. Choose
		// an unshadowed alias so keyless ordinal IDs remain reproducible.
		for _, candidate := range []string{"_rowid_", "rowid", "oid"} {
			shadowed := false
			for _, name := range names {
				if strings.EqualFold(name, candidate) {
					shadowed = true
					break
				}
			}
			if !shadowed {
				order = candidate
				break
			}
		}
		if order == "" {
			return nil, fmt.Errorf("dalgo2sqlite: keyless table %q shadows every hidden rowid alias", table)
		}
	}
	statement := "SELECT " + strings.Join(selects, ", ") + " FROM " + quoteIdentifier(table) + " ORDER BY " + order
	result, err := d.sqlDB.QueryContext(ctx, statement)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: source rows for %q: %w", table, err)
	}
	return &sourceRowsCursor{rows: result, names: names, decimal: decimal}, nil
}

func (cursor *sourceRowsCursor) Next() (dbschema.SourceRow, error) {
	if !cursor.rows.Next() {
		if err := cursor.rows.Err(); err != nil {
			return dbschema.SourceRow{}, err
		}
		return dbschema.SourceRow{}, io.EOF
	}
	count := len(cursor.names)
	cells := make([]any, count*2)
	dest := make([]any, len(cells))
	for i := range cells {
		dest[i] = &cells[i]
	}
	if err := cursor.rows.Scan(dest...); err != nil {
		return dbschema.SourceRow{}, err
	}
	row := dbschema.SourceRow{Values: make(map[string]any, count), StorageClasses: make(map[string]string, count)}
	for i, name := range cursor.names {
		class, ok := cells[count+i].(string)
		if !ok {
			return dbschema.SourceRow{}, fmt.Errorf("dalgo2sqlite: storage class for %q is %T", name, cells[count+i])
		}
		value := cells[i]
		if cursor.decimal[i] {
			switch typed := value.(type) {
			case int64:
				value = strconv.FormatInt(typed, 10)
			case float64:
				if math.IsNaN(typed) || math.IsInf(typed, 0) {
					return dbschema.SourceRow{}, fmt.Errorf("dalgo2sqlite: non-finite decimal %q", name)
				}
				value = strconv.FormatFloat(typed, 'g', -1, 64)
			}
		}
		row.Values[name] = value
		row.StorageClasses[name] = class
	}
	return row, nil
}

func (cursor *sourceRowsCursor) Close() error { return cursor.rows.Close() }
