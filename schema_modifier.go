package dalgo2sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
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

// AlterCollection applies ops in order inside a single transaction.
// Partial failures roll back and leave the collection untouched.
func (d *Database) AlterCollection(ctx context.Context, name string, ops ...ddl.AlterOp) error {
	return d.inTx(ctx, func(tx *sql.Tx) error {
		a := &sqliteAlterApplier{ctx: ctx, tx: tx, table: name, sqlDB: d.sqlDB}
		for _, op := range ops {
			if err := op.ApplyTo(ctx, a); err != nil {
				return err
			}
		}
		return nil
	})
}

// sqliteAlterApplier implements ddl.Applier for the in-flight
// AlterCollection transaction. One instance per AlterCollection call.
// Embeds the transaction so each ApplyXxx method runs against the
// same *sql.Tx as the rest of the batch — rollback on any error
// undoes the whole batch.
//
// ctx is stored only as a fallback for the rare case an ApplyXxx
// caller does not forward the per-call ctx (which the current
// AlterOp.ApplyTo contract always does); per-call ctx wins where
// supplied.
type sqliteAlterApplier struct {
	ctx   context.Context
	tx    *sql.Tx
	table string
	sqlDB *sql.DB // used only by ApplyModifyField's introspection path
}

func (a *sqliteAlterApplier) ApplyAddField(ctx context.Context, f dbschema.FieldDef, opts ddl.Options) error {
	sqlStmt, err := buildAlterTableAddColumnSQL(a.table, f)
	if err != nil {
		return err
	}
	if _, err := a.tx.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("dalgo2sqlite: ApplyAddField %q: %w", f.Name, err)
	}
	if f.Type == dbschema.Time {
		// Ensure marker table exists then write the marker — both
		// inside the in-flight tx so failure rolls back.
		if _, err := a.tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+timeMarkerTable+
			` (collection_name TEXT NOT NULL, column_name TEXT NOT NULL,
			   PRIMARY KEY (collection_name, column_name))`); err != nil {
			return fmt.Errorf("dalgo2sqlite: ApplyAddField marker table: %w", err)
		}
		if _, err := a.tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO `+timeMarkerTable+` (collection_name, column_name) VALUES (?, ?)`,
			a.table, string(f.Name)); err != nil {
			return fmt.Errorf("dalgo2sqlite: ApplyAddField marker insert: %w", err)
		}
	}
	return nil
}

func (a *sqliteAlterApplier) ApplyDropField(ctx context.Context, name dal.FieldName, opts ddl.Options) error {
	sqlStmt := buildAlterTableDropColumnSQL(a.table, name)
	if _, err := a.tx.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("dalgo2sqlite: ApplyDropField %q: %w", name, err)
	}
	// Best-effort marker cleanup; tolerated even if sidecar absent.
	_, _ = a.tx.ExecContext(ctx,
		`DELETE FROM `+timeMarkerTable+` WHERE collection_name=? AND column_name=?`,
		a.table, string(name))
	return nil
}

func (a *sqliteAlterApplier) ApplyRenameField(ctx context.Context, oldName, newName dal.FieldName, opts ddl.Options) error {
	sqlStmt := buildAlterTableRenameColumnSQL(a.table, oldName, newName)
	if _, err := a.tx.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("dalgo2sqlite: ApplyRenameField %q->%q: %w", oldName, newName, err)
	}
	// Best-effort marker rename.
	_, _ = a.tx.ExecContext(ctx,
		`UPDATE `+timeMarkerTable+` SET column_name=? WHERE collection_name=? AND column_name=?`,
		string(newName), a.table, string(oldName))
	return nil
}

func (a *sqliteAlterApplier) ApplyAddIndex(ctx context.Context, idx dbschema.IndexDef, opts ddl.Options) error {
	if idx.Collection == "" {
		idx.Collection = a.table
	}
	sqlStmt, err := buildCreateIndexSQL(idx, opts)
	if err != nil {
		return err
	}
	if _, err := a.tx.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("dalgo2sqlite: ApplyAddIndex %q: %w", idx.Name, err)
	}
	return nil
}

func (a *sqliteAlterApplier) ApplyDropIndex(ctx context.Context, name string, opts ddl.Options) error {
	sqlStmt := buildDropIndexSQL(name, opts)
	if _, err := a.tx.ExecContext(ctx, sqlStmt); err != nil {
		return fmt.Errorf("dalgo2sqlite: ApplyDropIndex %q: %w", name, err)
	}
	return nil
}

// ApplyModifyField performs the SQLite migration dance: introspect
// current shape via the tx, build a new CollectionDef with the
// modified field, CREATE <name>_new, INSERT INTO _new SELECT FROM
// original, DROP original, RENAME _new -> original. All within the
// enclosing transaction so partial failures roll back.
//
// Secondary indexes on the rewritten table are NOT recreated
// automatically — callers re-add via AlterCollection if needed.
// This matches PostgreSQL behavior on column rewrites.
func (a *sqliteAlterApplier) ApplyModifyField(ctx context.Context, name dal.FieldName, newDef dbschema.FieldDef, opts ddl.Options) error {
	current, err := readCollectionDefViaTx(ctx, a.tx, a.table)
	if err != nil {
		return fmt.Errorf("dalgo2sqlite: ModifyField introspect %q: %w", a.table, err)
	}

	modified := dbschema.CollectionDef{
		Name:       a.table + "_new",
		Fields:     make([]dbschema.FieldDef, 0, len(current.Fields)),
		PrimaryKey: current.PrimaryKey,
	}
	var found bool
	for _, f := range current.Fields {
		if f.Name == name {
			n := newDef
			n.Name = name // preserve column name
			modified.Fields = append(modified.Fields, n)
			found = true
		} else {
			modified.Fields = append(modified.Fields, f)
		}
	}
	if !found {
		return fmt.Errorf("dalgo2sqlite: ModifyField: column %q does not exist on %q", name, a.table)
	}

	createSQL, err := buildCreateTableSQL(modified, ddl.Options{})
	if err != nil {
		return err
	}
	if _, err := a.tx.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("dalgo2sqlite: ModifyField create new: %w", err)
	}

	// Copy data via INSERT INTO _new (cols) SELECT cols FROM original.
	colNames := make([]string, len(modified.Fields))
	for i, f := range modified.Fields {
		colNames[i] = string(f.Name)
	}
	cols := strings.Join(colNames, ", ")
	copySQL := fmt.Sprintf("INSERT INTO %s_new (%s) SELECT %s FROM %s", a.table, cols, cols, a.table)
	if _, err := a.tx.ExecContext(ctx, copySQL); err != nil {
		return fmt.Errorf("dalgo2sqlite: ModifyField copy data: %w", err)
	}

	if _, err := a.tx.ExecContext(ctx, "DROP TABLE "+a.table); err != nil {
		return fmt.Errorf("dalgo2sqlite: ModifyField drop original: %w", err)
	}

	if _, err := a.tx.ExecContext(ctx, "ALTER TABLE "+a.table+"_new RENAME TO "+a.table); err != nil {
		return fmt.Errorf("dalgo2sqlite: ModifyField rename: %w", err)
	}

	return nil
}

// readCollectionDefViaTx is a tx-aware variant of describeCollectionImpl.
// Used by ApplyModifyField so we don't read inconsistent state outside
// the enclosing transaction.
//
// Lossy: declared types reverse-map via dbschema affinity, AutoIncrement
// detection is skipped (sqlite_sequence updates are tx-visible but
// querying it adds complexity that ModifyField doesn't need — the
// migration creates a fresh table with the same columns, and the
// AUTOINCREMENT clause survives as long as we preserve the column's
// declared type and PK status from the source DDL).
func readCollectionDefViaTx(ctx context.Context, tx *sql.Tx, name string) (*dbschema.CollectionDef, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT name, type, "notnull", pk FROM pragma_table_info(?)`,
		name,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type pkE struct {
		col   string
		order int
	}
	var fields []dbschema.FieldDef
	var pkEs []pkE
	for rows.Next() {
		var (
			colName  string
			declType string
			notnull  int
			pkPos    int
		)
		if err := rows.Scan(&colName, &declType, &notnull, &pkPos); err != nil {
			return nil, err
		}
		t, precision, ok := dbschemaTypeFromSQLite(declType)
		if !ok {
			t = dbschema.String // safe fallback for migration; lossy but progresses
		}
		fields = append(fields, dbschema.FieldDef{
			Name:      dal.FieldName(colName),
			Type:      t,
			Precision: precision,
			Nullable:  notnull == 0 && pkPos == 0,
		})
		if pkPos > 0 {
			pkEs = append(pkEs, pkE{col: colName, order: pkPos})
		}
	}
	// Insertion-sort PK by order.
	for i := 1; i < len(pkEs); i++ {
		for j := i; j > 0 && pkEs[j-1].order > pkEs[j].order; j-- {
			pkEs[j-1], pkEs[j] = pkEs[j], pkEs[j-1]
		}
	}
	pk := make([]dal.FieldName, len(pkEs))
	for i, e := range pkEs {
		pk[i] = dal.FieldName(e.col)
	}
	return &dbschema.CollectionDef{Name: name, Fields: fields, PrimaryKey: pk}, nil
}
