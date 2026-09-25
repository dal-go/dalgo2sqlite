package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

func readForeignKeys(ctx context.Context, db *sql.DB, table string) ([]dbschema.ForeignKeyDef, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: foreign keys connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var enabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: foreign keys enforcement: %w", err)
	}
	state := dbschema.ForeignKeyEnforcementDisabled
	if enabled != 0 {
		state = dbschema.ForeignKeyEnforcementEnabled
	}
	rows, err := conn.QueryContext(ctx,
		`SELECT id, seq, "table", "from", "to", on_update, on_delete
		 FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: foreign keys for %q: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var keys []dbschema.ForeignKeyDef
	lastID := -1
	for rows.Next() {
		var id, seq int
		var target, from, onUpdate, onDelete string
		var to sql.NullString
		if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete); err != nil {
			return nil, fmt.Errorf("dalgo2sqlite: foreign key scan: %w", err)
		}
		if id != lastID {
			keys = append(keys, dbschema.ForeignKeyDef{
				Name: fmt.Sprintf("%s_fk_%d", table, id), ReferencedCollection: target,
				Enforcement: state, OnUpdate: onUpdate, OnDelete: onDelete,
			})
			lastID = id
		}
		key := &keys[len(keys)-1]
		key.Fields = append(key.Fields, dal.FieldName(from))
		if to.Valid && to.String != "" {
			key.ReferencedFields = append(key.ReferencedFields, dal.FieldName(to.String))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: foreign key rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: close foreign key rows: %w", err)
	}
	for i := range keys {
		if len(keys[i].ReferencedFields) != 0 {
			continue
		}
		pkRows, err := conn.QueryContext(ctx,
			`SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, keys[i].ReferencedCollection)
		if err != nil {
			return nil, fmt.Errorf("dalgo2sqlite: foreign key target primary key: %w", err)
		}
		for pkRows.Next() {
			var field string
			if err := pkRows.Scan(&field); err != nil {
				_ = pkRows.Close()
				return nil, fmt.Errorf("dalgo2sqlite: foreign key target primary key scan: %w", err)
			}
			keys[i].ReferencedFields = append(keys[i].ReferencedFields, dal.FieldName(field))
		}
		if err := pkRows.Err(); err != nil {
			_ = pkRows.Close()
			return nil, fmt.Errorf("dalgo2sqlite: foreign key target primary key rows: %w", err)
		}
		_ = pkRows.Close()
	}
	return keys, nil
}
