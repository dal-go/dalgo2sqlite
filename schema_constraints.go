package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// ListConstraints returns a best-effort survey of constraints on the
// table:
//   - The primary-key constraint (one row if any PK columns exist)
//   - Foreign-key constraints from PRAGMA foreign_key_list
//
// CHECK clauses and inline NOT NULL constraints are NOT enumerated
// (SQLite doesn't expose CHECK source portably). Callers read those
// from DescribeCollection.Fields.
func (d *Database) ListConstraints(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	var out []dbschema.ConstraintDef

	// Primary key — synthesize one entry if any PK columns exist.
	pkRows, err := d.sqlDB.QueryContext(ctx,
		`SELECT name FROM pragma_table_info(?) WHERE pk > 0 LIMIT 1`,
		ref.Name(),
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: ListConstraints pk probe: %w", err)
	}
	hasPK := pkRows.Next()
	_ = pkRows.Close()
	if hasPK {
		out = append(out, dbschema.ConstraintDef{
			Name: ref.Name() + "_pk",
			Type: "primary-key",
		})
	}

	// Foreign keys — collapse per-column rows to one entry per FK id.
	fkRows, err := d.sqlDB.QueryContext(ctx,
		`SELECT DISTINCT id FROM pragma_foreign_key_list(?) ORDER BY id`,
		ref.Name(),
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: ListConstraints fk probe: %w", err)
	}
	defer func() { _ = fkRows.Close() }()
	for fkRows.Next() {
		var id int
		if scanErr := fkRows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: ListConstraints fk scan: %w", scanErr)
		}
		out = append(out, dbschema.ConstraintDef{
			Name: fmt.Sprintf("%s_fk_%d", ref.Name(), id),
			Type: "foreign-key",
		})
	}
	return out, fkRows.Err()
}

// ListReferrers performs an O(N) scan: for each other user-defined
// table, query PRAGMA foreign_key_list and check whether any row
// references ref.Name.
func (d *Database) ListReferrers(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.Referrer, error) {
	tables, err := d.ListCollections(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []dbschema.Referrer
	for _, t := range tables {
		if t.Name() == ref.Name() {
			continue
		}
		fields, fkErr := referrerFields(ctx, d.sqlDB, t.Name(), ref.Name())
		if fkErr != nil {
			return nil, fkErr
		}
		if len(fields) > 0 {
			out = append(out, dbschema.Referrer{
				Collection: t,
				Fields:     fields,
			})
		}
	}
	return out, nil
}

// referrerFields returns the columns in `from` that reference `to`
// via foreign keys.
func referrerFields(ctx context.Context, db *sql.DB, from, to string) ([]dal.FieldName, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT "from" FROM pragma_foreign_key_list(?) WHERE "table"=?`,
		from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: pragma_foreign_key_list (%s->%s): %w", from, to, err)
	}
	defer func() { _ = rows.Close() }()
	var out []dal.FieldName
	for rows.Next() {
		var col string
		if scanErr := rows.Scan(&col); scanErr != nil {
			return nil, fmt.Errorf("dalgo2sqlite: referrerFields scan: %w", scanErr)
		}
		out = append(out, dal.FieldName(col))
	}
	return out, rows.Err()
}
