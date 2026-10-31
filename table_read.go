package dalgo2sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// TableReadLimits configures a derived per-cell limit and column ceiling before
// data SQL.
// A combined accepted-value bound of MaxBytes applies only when the host enforces
// a frozen query returning exactly the selected table's original N columns.
// Session is a generic read surface, not a query-shape or permission enforcer;
// arbitrary projections/expressions do not inherit that combined-value bound.
// The derived per-cell bound can refuse cells below MaxBytes; no truncation or
// guarantee of accepting every smaller row is implied. Encoded-row/driver/Go
// overhead is excluded. The host must also bound row count and serialized output.
// MaxBytes must be 1..256KiB and MaxColumns 1..128.
type TableReadLimits struct{ MaxBytes, MaxColumns uint32 }

// TableReadFacts describes one pinned main-table read. These are native naming
// and allocation facts, never permissions or proof of an authenticated source.
// Filename is SQLite's logical opened-file path; it is not OS inode/replacement
// attestation. A trusted host must own/fix its private file route and bind it to
// an independently authenticated source identity before using these facts.
type TableReadFacts struct {
	Filename, Namespace, Table string
	Columns                    []string
	MaxBytes, MaxColumns       uint32
	// MaxCellBytes is verified on the same native Conn after column discovery.
	MaxCellBytes uint32
	// Encoding is exactly UTF-8 in the only admitted first profile.
	Encoding string
}

// TableReadSpan owns a private mode=ro connection and rollback-only transaction.
// Its immutable session/compiler and facts remain on that handle until Close.
// Ingress cancellation invalidates the transaction; callers must still Close. It exposes no native/write/commit handle. Permission
// evaluation belongs to the host's secured DALgo session, not this provider.
type TableReadSpan struct {
	ctx     context.Context
	cancel  context.CancelFunc
	db      *sql.DB
	conn    *sql.Conn
	tx      *sql.Tx
	session dal.ReadSession
	facts   TableReadFacts
	closed  atomic.Bool
}

var tableReadName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var errTableReadProfile = errors.New("dalgo2sqlite: unsupported pinned table read profile")

type tableReadDependencies struct {
	open    func(string, string) (*sql.DB, error)
	limit   func(*sql.Conn, int, int) (int, error)
	session func(*sql.Tx, dalgo2sql.DbOptions) (dal.ReadSession, error)
}

// OpenTableRead opens an existing absolute file through a provider-built mode=ro
// URI. Only a UTF-8 file, explicit main namespace and exact simple ASCII ordinary table
// are supported; views, virtual/shadow/generated tables, attachments, implicit
// namespaces and case/quote/dot rewrites are refused. It does not authorize the
// table or authenticate filename. See TableReadFacts for the host's source duty.
//
// All metadata, query_only and native limits are established before data SQL.
// Facts is SQL-free thereafter. Close invalidates/cancels first, then rolls back
// and closes native storage; it never commits. The first Close caller owns any
// cleanup errors it observes (database/sql can consume auto-rollback errors).
// Concurrent/repeated Close calls return nil without waiting for
// that caller's native I/O. Hosts must report cleanup errors in their lifecycle
// channel, not append them to a completed response.
func OpenTableRead(ctx context.Context, filename, namespace, table string, limits TableReadLimits) (*TableReadSpan, error) {
	return openTableRead(ctx, filename, namespace, table, limits, tableReadDependencies{open: sql.Open, limit: sqlite.Limit, session: dalgo2sql.NewReadSession})
}

func openTableRead(ctx context.Context, filename, namespace, table string, limits TableReadLimits, deps tableReadDependencies) (span *TableReadSpan, err error) {
	if ctx == nil || !filepath.IsAbs(filename) || filepath.Clean(filename) != filename || namespace != "main" || (!tableReadName.MatchString(table) || strings.HasPrefix(strings.ToLower(table), "sqlite_")) || limits.MaxBytes == 0 || limits.MaxBytes > 256*1024 || limits.MaxColumns == 0 || limits.MaxColumns > 128 {
		return nil, errTableReadProfile
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	owned := &TableReadSpan{ctx: lifetime, cancel: cancel}
	defer func() {
		if err != nil {
			err = errors.Join(err, owned.Close())
		}
	}()
	uri := (&url.URL{Scheme: "file", Path: filename, RawQuery: "mode=ro"}).String()
	if owned.db, err = deps.open("sqlite", uri); err != nil {
		return nil, err
	}
	owned.db.SetMaxOpenConns(1)
	owned.db.SetMaxIdleConns(0)
	if owned.conn, err = owned.db.Conn(lifetime); err != nil {
		return nil, err
	}
	for _, bound := range []struct{ id, value int }{{sqlite3.SQLITE_LIMIT_LENGTH, int(limits.MaxBytes)}, {sqlite3.SQLITE_LIMIT_COLUMN, int(limits.MaxColumns)}} {
		if _, err = deps.limit(owned.conn, bound.id, bound.value); err != nil {
			return nil, err
		}
		var actual int
		if actual, err = deps.limit(owned.conn, bound.id, -1); err != nil {
			return nil, err
		}
		if actual != bound.value {
			return nil, errTableReadProfile
		}
	}
	if _, err = owned.conn.ExecContext(lifetime, "PRAGMA query_only=ON"); err != nil {
		return nil, err
	}
	var queryOnly int
	if err = owned.conn.QueryRowContext(lifetime, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		return nil, err
	}
	if queryOnly != 1 {
		return nil, errTableReadProfile
	}
	// The modernc driver does not implement database/sql ReadOnly options. There
	// is deliberately no retry/fallback: native mode=ro/query_only establish the
	// read-only effects, and this explicit transaction is always rolled back.
	if owned.tx, err = owned.conn.BeginTx(lifetime, nil); err != nil {
		return nil, err
	}
	if owned.facts, err = inspectTableRead(lifetime, owned.tx, filename, namespace, table, limits); err != nil {
		return nil, err
	}
	// SQLite's LENGTH bound does not cap the combined cells of every existing
	// read row. A derived per-cell bound also limits the sum of accepted native
	// cell bytes. Integers/reals occupy at most eight bytes; narrower budgets
	// cannot establish that bound and are refused before data SQL.
	cellBytes := limits.MaxBytes / uint32(len(owned.facts.Columns))
	if cellBytes < 8 {
		return nil, errTableReadProfile
	}
	if _, err = deps.limit(owned.conn, sqlite3.SQLITE_LIMIT_LENGTH, int(cellBytes)); err != nil {
		return nil, err
	}
	var actualCellBytes int
	if actualCellBytes, err = deps.limit(owned.conn, sqlite3.SQLITE_LIMIT_LENGTH, -1); err != nil {
		return nil, err
	}
	if actualCellBytes != int(cellBytes) {
		return nil, errTableReadProfile
	}
	owned.facts.MaxCellBytes = cellBytes
	owned.session, err = deps.session(owned.tx, dalgo2sql.DbOptions{StructuredQueryDialect: "sqlite", IdentifierCase: dalgo2sql.IdentifierCaseExact, PreserveNativeValues: true})
	if err != nil {
		return nil, err
	}
	if nilTableReadValue(owned.session) {
		return nil, errTableReadProfile
	}
	owned.session = &tableReadSession{owner: owned, inner: owned.session}
	if err = lifetime.Err(); err != nil {
		return nil, err
	}
	return owned, nil
}

// Session returns the same immutable DALgo read surface. It is nil after
// invalidation. The host must wrap it with its own central access policy.
func (s *TableReadSpan) Session() dal.ReadSession {
	if s == nil || s.ctx == nil || s.closed.Load() || s.ctx.Err() != nil {
		return nil
	}
	return s.session
}

// Facts returns a defensive metadata copy and SQL-free liveness check. It does
// not acquire a lock used by a reader or native Close, and never queries SQLite.
func (s *TableReadSpan) Facts(ctx context.Context) (TableReadFacts, error) {
	if s == nil || s.ctx == nil || ctx == nil {
		return TableReadFacts{}, errTableReadProfile
	}
	if err := ctx.Err(); err != nil {
		return TableReadFacts{}, err
	}
	if err := s.ctx.Err(); err != nil {
		return TableReadFacts{}, err
	}
	if s.closed.Load() {
		return TableReadFacts{}, errTableReadProfile
	}
	facts := s.facts
	facts.Columns = append([]string(nil), facts.Columns...)
	return facts, nil
}

// Close invalidates before native cleanup. No ownership mutex spans native I/O.
func (s *TableReadSpan) Close() error {
	if s == nil || s.closed.Swap(true) {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	var failures []error
	if s.tx != nil {
		if err := s.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			failures = append(failures, err)
		}
	}
	if s.conn != nil {
		failures = append(failures, s.conn.Close())
	}
	if s.db != nil {
		failures = append(failures, s.db.Close())
	}
	return errors.Join(failures...)
}

func inspectTableRead(ctx context.Context, tx *sql.Tx, filename, namespace, table string, limits TableReadLimits) (TableReadFacts, error) {
	if err := inspectTableReadDatabases(ctx, tx, filename); err != nil {
		return TableReadFacts{}, err
	}
	// SQLite text conversion from a UTF-16 file can expand beyond LENGTH's
	// stored-byte limit. Only UTF-8 files establish the returned-byte bound.
	var encoding string
	if err := tx.QueryRowContext(ctx, "PRAGMA main.encoding").Scan(&encoding); err != nil {
		return TableReadFacts{}, err
	}
	if encoding != "UTF-8" {
		return TableReadFacts{}, errTableReadProfile
	}
	var schema, name, kind string
	var ncol, withoutRowID, strict int
	// Names are already constrained to plain ASCII components. The quoted pragma
	// argument is an exact metadata lookup, never caller-supplied SQL syntax.
	query := `PRAGMA main.table_list("` + table + `")`
	if err := tx.QueryRowContext(ctx, query).Scan(&schema, &name, &kind, &ncol, &withoutRowID, &strict); err != nil {
		return TableReadFacts{}, err
	}
	if schema != namespace || name != table || kind != "table" || ncol < 1 || ncol > int(limits.MaxColumns) {
		return TableReadFacts{}, errTableReadProfile
	}
	columns, err := inspectTableReadColumns(ctx, tx, table, limits)
	if err != nil {
		return TableReadFacts{}, err
	}
	if len(columns) != ncol {
		return TableReadFacts{}, errTableReadProfile
	}
	return TableReadFacts{Filename: filename, Namespace: namespace, Table: table, Columns: columns, MaxBytes: limits.MaxBytes, MaxColumns: limits.MaxColumns, Encoding: encoding}, nil
}

func inspectTableReadDatabases(ctx context.Context, tx *sql.Tx, filename string) (err error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	seen := false
	for rows.Next() {
		var sequence int
		var name, file string
		if err = rows.Scan(&sequence, &name, &file); err != nil {
			return err
		}
		if seen || sequence != 0 || name != "main" || file != filename {
			return fmt.Errorf("%w: native database path or namespace differs", errTableReadProfile)
		}
		seen = true
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !seen {
		return errTableReadProfile
	}
	return nil
}

func inspectTableReadColumns(ctx context.Context, tx *sql.Tx, table string, limits TableReadLimits) (columns []string, err error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA main.table_xinfo("`+table+`")`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	seen := make(map[string]struct{})
	for rows.Next() {
		var id, notNull, key, hidden int
		var name, kind string
		var defaultValue sql.NullString
		if err = rows.Scan(&id, &name, &kind, &notNull, &defaultValue, &key, &hidden); err != nil {
			return nil, err
		}
		if id != len(columns) || hidden != 0 || !tableReadName.MatchString(name) || len(columns) >= int(limits.MaxColumns) {
			return nil, errTableReadProfile
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, errTableReadProfile
		}
		seen[name] = struct{}{}
		columns = append(columns, name)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("%w: no ordinary columns", errTableReadProfile)
	}
	return columns, nil
}

func nilTableReadValue(session any) bool {
	if session == nil {
		return true
	}
	value := reflect.ValueOf(session)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
