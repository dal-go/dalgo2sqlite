package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dal-go/dalgo/dbschema"
)

// readSourceDefinition retains SQLite catalog details that the portable
// CollectionDef intentionally cannot model. SQL text is metadata, never
// executed by this reader or by a generic exporter.
func readSourceDefinition(ctx context.Context, db *sql.DB, table string) (*dbschema.SourceDefinition, error) {
	if err := rejectHiddenSourceColumns(ctx, db, table); err != nil {
		return nil, err
	}
	source := &dbschema.SourceDefinition{Dialect: "sqlite"}
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&source.CreateSQL); err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: source DDL for %q: %w", table, err)
	}
	cols, err := db.QueryContext(ctx,
		`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: source columns for %q: %w", table, err)
	}
	for cols.Next() {
		var name, declared string
		var notNull, pk int
		var defaultSQL sql.NullString
		if err := cols.Scan(&name, &declared, &notNull, &defaultSQL, &pk); err != nil {
			_ = cols.Close()
			return nil, fmt.Errorf("dalgo2sqlite: source column scan for %q: %w", table, err)
		}
		column := dbschema.SourceColumnDef{Name: name, DeclaredType: declared, NotNull: notNull != 0, PrimaryKeyPosition: pk}
		if defaultSQL.Valid {
			value := defaultSQL.String
			column.DefaultSQL = &value
		}
		source.Columns = append(source.Columns, column)
	}
	if err := cols.Err(); err != nil {
		_ = cols.Close()
		return nil, fmt.Errorf("dalgo2sqlite: source column rows for %q: %w", table, err)
	}
	_ = cols.Close()
	indexes, err := db.QueryContext(ctx,
		`SELECT name, "unique", origin, partial FROM pragma_index_list(?) ORDER BY seq`, table)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: source indexes for %q: %w", table, err)
	}
	for indexes.Next() {
		var index dbschema.SourceIndexDef
		var unique, partial int
		if err := indexes.Scan(&index.Name, &unique, &index.Origin, &partial); err != nil {
			_ = indexes.Close()
			return nil, fmt.Errorf("dalgo2sqlite: source index scan for %q: %w", table, err)
		}
		index.Unique, index.Partial = unique != 0, partial != 0
		var createSQL sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, index.Name).Scan(&createSQL); err != nil {
			_ = indexes.Close()
			return nil, fmt.Errorf("dalgo2sqlite: index DDL for %q: %w", index.Name, err)
		}
		if createSQL.Valid {
			index.CreateSQL = createSQL.String
		}
		terms, err := db.QueryContext(ctx,
			`SELECT seqno, name, "desc", coll, "key" FROM pragma_index_xinfo(?) ORDER BY seqno`, index.Name)
		if err != nil {
			_ = indexes.Close()
			return nil, fmt.Errorf("dalgo2sqlite: index terms for %q: %w", index.Name, err)
		}
		for terms.Next() {
			var term dbschema.SourceIndexColumnDef
			var name, collation sql.NullString
			var descending, key int
			if err := terms.Scan(&term.Position, &name, &descending, &collation, &key); err != nil {
				_ = terms.Close()
				_ = indexes.Close()
				return nil, fmt.Errorf("dalgo2sqlite: index term scan for %q: %w", index.Name, err)
			}
			if name.Valid {
				value := name.String
				term.Name = &value
			}
			term.Descending, term.Key, term.Collation = descending != 0, key != 0, collation.String
			index.Columns = append(index.Columns, term)
		}
		if err := terms.Err(); err != nil {
			_ = terms.Close()
			_ = indexes.Close()
			return nil, fmt.Errorf("dalgo2sqlite: index term rows for %q: %w", index.Name, err)
		}
		_ = terms.Close()
		source.Indexes = append(source.Indexes, index)
	}
	if err := indexes.Err(); err != nil {
		_ = indexes.Close()
		return nil, fmt.Errorf("dalgo2sqlite: source index rows for %q: %w", table, err)
	}
	_ = indexes.Close()
	return source, nil
}

// pragma_table_info omits generated and hidden columns. Until the portable
// export contract can represent their computation, fail rather than silently
// publish an incomplete source schema or row.
func rejectHiddenSourceColumns(ctx context.Context, db *sql.DB, table string) error {
	rows, err := db.QueryContext(ctx,
		`SELECT name, hidden FROM pragma_table_xinfo(?) WHERE hidden != 0 ORDER BY cid`, table)
	if err != nil {
		return fmt.Errorf("dalgo2sqlite: hidden source columns for %q: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var name string
		var hidden int
		if err := rows.Scan(&name, &hidden); err != nil {
			return fmt.Errorf("dalgo2sqlite: hidden source column scan for %q: %w", table, err)
		}
		return fmt.Errorf("dalgo2sqlite: source table %q has generated or hidden column %q (kind %d), unsupported for lossless export", table, name, hidden)
	}
	return rows.Err()
}

// ListSourceViews lists view definitions once per database. View SQL is
// descriptive metadata; views are not materialized as collection records.
func (d *Database) ListSourceViews(ctx context.Context) ([]dbschema.SourceViewDef, error) {
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT name, sql FROM sqlite_master WHERE type='view' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: source views: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var views []dbschema.SourceViewDef
	for rows.Next() {
		var view dbschema.SourceViewDef
		if err := rows.Scan(&view.Name, &view.CreateSQL); err != nil {
			return nil, fmt.Errorf("dalgo2sqlite: source view scan: %w", err)
		}
		columns, err := d.sqlDB.QueryContext(ctx,
			`SELECT name FROM pragma_table_info(?) ORDER BY cid`, view.Name)
		if err != nil {
			return nil, fmt.Errorf("dalgo2sqlite: source view columns for %q: %w", view.Name, err)
		}
		for columns.Next() {
			var name string
			if err := columns.Scan(&name); err != nil {
				_ = columns.Close()
				return nil, fmt.Errorf("dalgo2sqlite: source view column scan for %q: %w", view.Name, err)
			}
			view.Columns = append(view.Columns, name)
		}
		if err := columns.Err(); err != nil {
			_ = columns.Close()
			return nil, fmt.Errorf("dalgo2sqlite: source view column rows for %q: %w", view.Name, err)
		}
		_ = columns.Close()
		views = append(views, view)
	}
	return views, rows.Err()
}
