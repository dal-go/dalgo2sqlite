package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
)

// CreateCollection creates a table and its inline indexes transactionally.
// On any error, the transaction rolls back and no schema state remains.
func (d *Database) CreateCollection(ctx context.Context, c dbschema.CollectionDef, opts ...ddl.Option) error {
	o := ddl.ResolveOptions(opts...)
	createSQL, err := buildCreateTableSQL(c, o)
	if err != nil {
		return err
	}
	indexSQLs := make([]string, 0, len(c.Indexes))
	for _, idx := range c.Indexes {
		if idx.Collection == "" {
			idx.Collection = c.Name
		}
		s, ierr := buildCreateIndexSQL(idx, o)
		if ierr != nil {
			return ierr
		}
		indexSQLs = append(indexSQLs, s)
	}

	timeCols := timeColumnsOf(c)

	return d.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, createSQL); err != nil {
			return fmt.Errorf("dalgo2sqlite: CreateCollection exec %q: %w", createSQL, err)
		}
		for _, s := range indexSQLs {
			if _, err := tx.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("dalgo2sqlite: CreateCollection index exec %q: %w", s, err)
			}
		}
		if len(timeCols) > 0 {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+timeMarkerTable+
				` (collection_name TEXT NOT NULL, column_name TEXT NOT NULL,
				   PRIMARY KEY (collection_name, column_name))`); err != nil {
				return fmt.Errorf("dalgo2sqlite: ensure time marker table: %w", err)
			}
			for _, col := range timeCols {
				if _, err := tx.ExecContext(ctx,
					`INSERT OR IGNORE INTO `+timeMarkerTable+` (collection_name, column_name) VALUES (?, ?)`,
					c.Name, col); err != nil {
					return fmt.Errorf("dalgo2sqlite: insert time marker (%s.%s): %w", c.Name, col, err)
				}
			}
		}
		return nil
	})
}

func timeColumnsOf(c dbschema.CollectionDef) []string {
	var out []string
	for _, f := range c.Fields {
		if f.Type == dbschema.Time {
			out = append(out, string(f.Name))
		}
	}
	return out
}

func (d *Database) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("dalgo2sqlite: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("dalgo2sqlite: commit tx: %w", err)
	}
	return nil
}

// DropCollection drops the table; SQLite cascades to its indexes.
func (d *Database) DropCollection(ctx context.Context, name string, opts ...ddl.Option) error {
	o := ddl.ResolveOptions(opts...)
	sqlStmt := buildDropTableSQL(name, o)
	return d.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("dalgo2sqlite: DropCollection exec: %w", err)
		}
		// Best-effort sidecar cleanup; tolerate absent sidecar table.
		_, _ = tx.ExecContext(ctx,
			`DELETE FROM `+timeMarkerTable+` WHERE collection_name=?`,
			name)
		return nil
	})
}

// AlterCollection is BLOCKED on upstream dalgo Applier — see plan
// pre-flight. Returns a typed not-implemented error.
func (d *Database) AlterCollection(ctx context.Context, name string, ops ...ddl.AlterOp) error {
	return fmt.Errorf("dalgo2sqlite: AlterCollection blocked on upstream dalgo ddl.Applier interface (see plan)")
}
