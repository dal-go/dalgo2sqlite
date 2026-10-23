package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
	// SQLite finds the table a foreign key names in any letter case: the key names
	// the table as the database spells it, when it holds one.
	spelled, err := tableSpellings(ctx, conn, keys)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		if name, ok := spelled[foldASCII(keys[i].ReferencedCollection)]; ok {
			keys[i].ReferencedCollection = name
		}
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

// foldASCII lower-cases the ASCII letters of name and no other, as SQLite's NOCASE
// compares names.
func foldASCII(name string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, name)
}

// tableSpellings is the tables of the database by their names folded as SQLite
// compares them (foldASCII), read only when there are keys to resolve.
func tableSpellings(ctx context.Context, conn *sql.Conn, keys []dbschema.ForeignKeyDef) (map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: foreign key target tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	spelled := map[string]string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("dalgo2sqlite: foreign key target table scan: %w", err)
		}
		spelled[foldASCII(name)] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dalgo2sqlite: foreign key target table rows: %w", err)
	}
	return spelled, nil
}
