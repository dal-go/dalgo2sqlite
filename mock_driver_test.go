package dalgo2sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
)

type driverHooks struct {
	connectErr   error
	queryHook    func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error, bool)
	execHook     func(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error, bool)
	beginHook    func(ctx context.Context, opts driver.TxOptions) (driver.Tx, error, bool)
	commitHook   func() (error, bool)
	rollbackHook func() (error, bool)
}

type hookConnector struct {
	baseConnector driver.Connector
	hooks         *driverHooks
}

func (c *hookConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.hooks != nil && c.hooks.connectErr != nil {
		return nil, c.hooks.connectErr
	}
	conn, err := c.baseConnector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &hookConn{Conn: conn, hooks: c.hooks}, nil
}

func (c *hookConnector) Driver() driver.Driver {
	return &sqlite.Driver{}
}

type hookConn struct {
	driver.Conn
	hooks *driverHooks
}

func (c *hookConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.hooks != nil && c.hooks.queryHook != nil {
		if rows, err, ok := c.hooks.queryHook(ctx, query, args); ok {
			return rows, err
		}
	}
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, query, args)
	}
	return nil, errors.New("QueryContext not supported")
}

func (c *hookConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.hooks != nil && c.hooks.execHook != nil {
		if res, err, ok := c.hooks.execHook(ctx, query, args); ok {
			return res, err
		}
	}
	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, query, args)
	}
	return nil, errors.New("ExecContext not supported")
}

func (c *hookConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.hooks != nil && c.hooks.beginHook != nil {
		if tx, err, ok := c.hooks.beginHook(ctx, opts); ok {
			return tx, err
		}
	}
	if bt, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err := bt.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &hookTx{Tx: tx, hooks: c.hooks}, nil
	}
	return nil, errors.New("driver connection does not support BeginTx")
}

type hookTx struct {
	driver.Tx
	hooks *driverHooks
}

func (t *hookTx) Commit() error {
	if t.hooks != nil && t.hooks.commitHook != nil {
		if err, ok := t.hooks.commitHook(); ok {
			return err
		}
	}
	return t.Tx.Commit()
}

func (t *hookTx) Rollback() error {
	if t.hooks != nil && t.hooks.rollbackHook != nil {
		if err, ok := t.hooks.rollbackHook(); ok {
			return err
		}
	}
	return t.Tx.Rollback()
}

type mockRows struct {
	cols     []string
	rows     [][]driver.Value
	pos      int
	nextErr  error
	closeErr error
}

func (m *mockRows) Columns() []string { return m.cols }
func (m *mockRows) Close() error      { return m.closeErr }
func (m *mockRows) Next(dest []driver.Value) error {
	if m.pos >= len(m.rows) {
		if m.nextErr != nil {
			return m.nextErr
		}
		return io.EOF
	}
	copy(dest, m.rows[m.pos])
	m.pos++
	return nil
}

func newHookDB(t *testing.T, hooks *driverHooks) (*sql.DB, *Database) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hook.db")
	baseConnector, err := sqlite.NewConnector(dbPath)
	if err != nil {
		t.Fatalf("sqlite.NewConnector: %v", err)
	}
	conn := &hookConnector{baseConnector: baseConnector, hooks: hooks}
	sqlDB := sql.OpenDB(conn)
	t.Cleanup(func() { _ = sqlDB.Close() })
	d := &Database{
		sqlDB:  sqlDB,
		dbPath: dbPath,
	}
	return sqlDB, d
}
