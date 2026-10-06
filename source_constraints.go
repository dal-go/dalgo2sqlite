package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dal-go/dalgo/dbschema"
)

var _ dbschema.SourceConstraintChecker = (*Database)(nil)

// CheckSourceConstraints asks SQLite to verify the source's table/index/CHECK
// integrity and foreign keys with SQLite's own affinity and collation rules.
// It checks stored rows; it does not claim the export target enforces them.
func (d *Database) CheckSourceConstraints(ctx context.Context) (resultErr error) {
	conn, err := d.sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("dalgo2sqlite: source constraint connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var ignoreChecks int
	if err := conn.QueryRowContext(ctx, `PRAGMA ignore_check_constraints`).Scan(&ignoreChecks); err != nil {
		return fmt.Errorf("dalgo2sqlite: source CHECK enforcement state: %w", err)
	}
	if ignoreChecks != 0 {
		if _, err := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=OFF`); err != nil {
			return fmt.Errorf("dalgo2sqlite: enable source CHECK validation: %w", err)
		}
		defer func() {
			if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=ON`); err != nil && resultErr == nil {
				resultErr = fmt.Errorf("dalgo2sqlite: restore source CHECK setting: %w", err)
			}
		}()
	}
	integrity, err := conn.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("dalgo2sqlite: source integrity check: %w", err)
	}
	seenOK := false
	for integrity.Next() {
		var result string
		if err := integrity.Scan(&result); err != nil {
			_ = integrity.Close()
			return fmt.Errorf("dalgo2sqlite: source integrity result: %w", err)
		}
		if result != "ok" {
			_ = integrity.Close()
			return fmt.Errorf("dalgo2sqlite: source integrity violation: %s", result)
		}
		seenOK = true
	}
	if err := integrity.Err(); err != nil {
		_ = integrity.Close()
		return fmt.Errorf("dalgo2sqlite: source integrity rows: %w", err)
	}
	_ = integrity.Close()
	if !seenOK {
		return fmt.Errorf("dalgo2sqlite: source integrity check returned no result")
	}
	foreignKeys, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("dalgo2sqlite: source foreign-key check: %w", err)
	}
	defer func() { _ = foreignKeys.Close() }()
	if foreignKeys.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var id int
		if err := foreignKeys.Scan(&table, &rowID, &parent, &id); err != nil {
			return fmt.Errorf("dalgo2sqlite: source foreign-key violation scan: %w", err)
		}
		return fmt.Errorf("dalgo2sqlite: source foreign-key violation in %q rowid %v references %q constraint %d", table, rowID, parent, id)
	}
	if err := foreignKeys.Err(); err != nil {
		return fmt.Errorf("dalgo2sqlite: source foreign-key rows: %w", err)
	}
	return nil
}
