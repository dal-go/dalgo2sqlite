package dalgo2sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func tableReadFixture(t *testing.T, script string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "owned.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(script); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func mustTableSpan(t *testing.T, ctx context.Context, file, table string, limits TableReadLimits) *TableReadSpan {
	t.Helper()
	span, err := OpenTableRead(ctx, file, "main", table, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := span.Close(); err != nil {
			t.Error(err)
		}
	})
	return span
}

func TestTableReadNativeValuesIdentityAndReadonlyEffects(t *testing.T) {
	file := tableReadFixture(t, `CREATE TABLE orders(id INTEGER,value);INSERT INTO orders VALUES(1,NULL),(2,9007199254740993),(3,2.5),(4,'text'),(5,x'00ff');CREATE TABLE unlisted(value);INSERT INTO unlisted VALUES('secret');`)
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	span := mustTableSpan(t, context.Background(), file, "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128})
	facts, err := span.Facts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if facts.Filename != file || facts.Namespace != "main" || facts.Table != "orders" || facts.Encoding != "UTF-8" || facts.MaxCellBytes != 128*1024 || !reflect.DeepEqual(facts.Columns, []string{"id", "value"}) {
		t.Fatalf("facts=%#v", facts)
	}
	facts.Columns[0] = "forged"
	again, err := span.Facts(context.Background())
	if err != nil || again.Columns[0] != "id" {
		t.Fatalf("facts mutated: %#v, %v", again, err)
	}
	session := span.Session()
	if session == nil {
		t.Fatal("missing session")
	}
	if _, ok := session.(dal.WriteSession); ok {
		t.Fatal("session exposes write methods")
	}
	if _, ok := session.(dal.Transaction); ok {
		t.Fatal("session exposes transaction")
	}
	if _, ok := session.(io.Closer); ok {
		t.Fatal("session exposes native handle lifetime")
	}
	q := dal.From(dal.NewQualifiedRootCollectionRef("main", "orders", "")).NewQuery().Limit(6).SelectIntoRecordset()
	reader, err := session.ExecuteQueryToRecordsetReader(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]any{1: nil, 2: int64(9007199254740993), 3: float64(2.5), 4: "text", 5: []byte{0, 255}}
	seen := 0
	for {
		row, rs, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		id, err := row.GetValueByIndex(0, rs)
		if err != nil {
			t.Fatal(err)
		}
		value, err := row.GetValueByIndex(1, rs)
		if err != nil {
			t.Fatal(err)
		}
		expected, ok := want[id.(int64)]
		if !ok || !reflect.DeepEqual(value, expected) {
			t.Fatalf("native row=%v %#v (%T)", id, value, value)
		}
		seen++
		if _, err = span.Facts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if seen != len(want) {
		t.Fatalf("rows=%d", seen)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	var only int
	if err := span.tx.QueryRow("PRAGMA query_only").Scan(&only); err != nil || only != 1 {
		t.Fatalf("query_only=%d,%v", only, err)
	}
	for _, test := range []struct{ id, want int }{{sqlite3.SQLITE_LIMIT_LENGTH, 128 * 1024}, {sqlite3.SQLITE_LIMIT_COLUMN, 128}} {
		value, err := sqlite.Limit(span.conn, test.id, -1)
		if err != nil || value != test.want {
			t.Fatalf("native limit=%d,%v", value, err)
		}
	}
	for _, statement := range []string{"INSERT INTO orders VALUES(6,'no')", "UPDATE orders SET value='no'", "DELETE FROM orders", "CREATE TABLE forbidden(id)", "DROP TABLE orders"} {
		_, err := span.tx.Exec(statement)
		var native *sqlite.Error
		if !errors.As(err, &native) || native.Code()&255 != sqlite3.SQLITE_READONLY {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := span.Close(); err != nil {
		t.Fatal(err)
	}
	if span.Session() != nil {
		t.Fatal("closed session published")
	}
	if _, err := span.Facts(context.Background()); err == nil {
		t.Fatal("closed facts published")
	}
	if reader, err := session.ExecuteQueryToRecordsetReader(context.Background(), q); reader != nil || err == nil {
		t.Fatalf("closed native session=%#v,%v", reader, err)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(original) != sha256.Sum256(after) {
		t.Fatal("owned sample file changed")
	}
}

func TestTableReadNativeUnsupportedObjectsAndNames(t *testing.T) {
	file := tableReadFixture(t, `CREATE TABLE orders(id INTEGER);CREATE VIEW summary AS SELECT * FROM orders;CREATE TABLE generated(id INTEGER,value INTEGER GENERATED ALWAYS AS(id+1));CREATE VIRTUAL TABLE search USING fts5(value);CREATE TABLE odd("bad name" TEXT);`)
	for _, table := range []string{"missing", "sqlite_schema", "Orders", "summary", "generated", "search", "search_data", "odd", "orders.name", "\"orders\"", "orders--", ""} {
		t.Run(table, func(t *testing.T) {
			span, err := OpenTableRead(context.Background(), file, "main", table, TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128})
			if span != nil || err == nil {
				if span != nil {
					_ = span.Close()
				}
				t.Fatalf("unsupported %q admitted", table)
			}
		})
	}
	for _, namespace := range []string{"", "Main", "temp", "attached", "main.other"} {
		if span, err := OpenTableRead(context.Background(), file, namespace, "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}); span != nil || err == nil {
			t.Fatalf("namespace %q admitted", namespace)
		}
	}
}

func TestTableReadNativeLimitsBeforeDataRows(t *testing.T) {
	file := tableReadFixture(t, `CREATE TABLE orders(value TEXT);INSERT INTO orders VALUES('`+strings.Repeat("z", 1000)+`')`)
	span := mustTableSpan(t, context.Background(), file, "orders", TableReadLimits{MaxBytes: 512, MaxColumns: 128})
	q := dal.From(dal.NewQualifiedRootCollectionRef("main", "orders", "")).NewQuery().Limit(2).SelectIntoRecordset()
	reader, err := span.Session().ExecuteQueryToRecordsetReader(context.Background(), q)
	if err == nil {
		_, _, err = reader.Next()
		if closeErr := reader.Close(); closeErr != nil && !errors.Is(closeErr, err) {
			t.Fatal(closeErr)
		}
	}
	var native *sqlite.Error
	if !errors.As(err, &native) || native.Code()&255 != sqlite3.SQLITE_TOOBIG {
		t.Fatalf("oversized native cell err=%v", err)
	}
	cols := make([]string, 129)
	for i := range cols {
		cols[i] = "c" + strings.Repeat("x", i+1) + " TEXT"
	}
	wide := tableReadFixture(t, "CREATE TABLE wide("+strings.Join(cols, ",")+")")
	if s, err := OpenTableRead(context.Background(), wide, "main", "wide", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}); s != nil || err == nil {
		t.Fatal("wide native table admitted")
	}
}

func TestTableReadCancellationAndConcurrentClose(t *testing.T) {
	file := tableReadFixture(t, "CREATE TABLE orders(id INTEGER);INSERT INTO orders VALUES(1)")
	ctx, cancel := context.WithCancel(context.Background())
	span := mustTableSpan(t, ctx, file, "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128})
	session := span.Session()
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		reader, err := session.ExecuteQueryToRecordsetReader(ctx, dal.NewTextQuery("WITH RECURSIVE counter(n) AS(VALUES(0) UNION ALL SELECT n+1 FROM counter) SELECT sum(n) FROM counter", nil))
		if reader != nil {
			_ = reader.Close()
		}
		finished <- err
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	cancel()
	if _, err := span.Facts(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled facts=%v", err)
	}
	if span.Session() != nil {
		t.Fatal("cancelled session exposed")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := span.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("native Close stalled")
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled native query succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("native query ignored cancellation")
	}
	if span.db.Stats().OpenConnections != 0 {
		t.Fatal("native connection remained open")
	}
}

func TestTableReadRejectsInvalidInputAndZeroValues(t *testing.T) {
	limits := TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}
	if s, err := OpenTableRead(nil, "/missing", "main", "orders", limits); s != nil || err == nil {
		t.Fatal("nil context admitted")
	}
	for _, file := range []string{"relative.db", "/tmp/../missing.db", ""} {
		if s, err := OpenTableRead(context.Background(), file, "main", "orders", limits); s != nil || err == nil {
			t.Fatal("invalid filename admitted")
		}
	}
	for _, bad := range []TableReadLimits{{}, {MaxBytes: 1, MaxColumns: 129}, {MaxBytes: 256*1024 + 1, MaxColumns: 128}, {MaxBytes: 1}, {MaxColumns: 1}} {
		if s, err := OpenTableRead(context.Background(), "/missing", "main", "orders", bad); s != nil || err == nil {
			t.Fatal("invalid limits admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := OpenTableRead(ctx, "/missing", "main", "orders", limits); s != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled opener=%#v,%v", s, err)
	}
	if s, err := OpenTableRead(context.Background(), filepath.Join(t.TempDir(), "absent.db"), "main", "orders", limits); s != nil || err == nil {
		t.Fatal("mode=ro created absent database")
	}
	for _, span := range []*TableReadSpan{nil, {}} {
		if span.Session() != nil {
			t.Fatal("zero session")
		}
		if _, err := span.Facts(context.Background()); err == nil {
			t.Fatal("zero facts")
		}
		if err := span.Close(); err != nil {
			t.Fatal(err)
		}
	}
	file := tableReadFixture(t, "CREATE TABLE orders(id INTEGER)")
	span := mustTableSpan(t, context.Background(), file, "orders", limits)
	if _, err := span.Facts(nil); err == nil {
		t.Fatal("nil context facts")
	}
	if _, err := span.Facts(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored caller cancellation")
	}
}

func TestTableReadNativeBlobAndWholeRowLimits(t *testing.T) {
	for _, test := range []struct{ name, script string }{
		{"blob", `CREATE TABLE orders(value BLOB);INSERT INTO orders VALUES(zeroblob(1000))`},
		{"whole-row", `CREATE TABLE orders(a TEXT,b TEXT);INSERT INTO orders VALUES('` + strings.Repeat("a", 300) + `','` + strings.Repeat("b", 300) + `')`},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := tableReadFixture(t, test.script)
			span := mustTableSpan(t, context.Background(), file, "orders", TableReadLimits{MaxBytes: 512, MaxColumns: 128})
			q := dal.From(dal.NewQualifiedRootCollectionRef("main", "orders", "")).NewQuery().Limit(2).SelectIntoRecordset()
			reader, err := span.Session().ExecuteQueryToRecordsetReader(context.Background(), q)
			if err == nil {
				_, _, err = reader.Next()
				if closeErr := reader.Close(); closeErr != nil && !errors.Is(closeErr, err) {
					t.Fatal(closeErr)
				}
			}
			var native *sqlite.Error
			if !errors.As(err, &native) || native.Code()&255 != sqlite3.SQLITE_TOOBIG {
				t.Fatalf("native bounded row/cell=%v", err)
			}
		})
	}
}

func TestTableReadNativeTempAndAttachedMetadataRefused(t *testing.T) {
	for _, kind := range []string{"temp", "attached"} {
		t.Run(kind, func(t *testing.T) {
			file := tableReadFixture(t, "CREATE TABLE orders(id INTEGER);INSERT INTO orders VALUES(1)")
			db, err := sql.Open("sqlite", file)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			}()
			if kind == "temp" {
				if _, err := conn.ExecContext(context.Background(), "CREATE TEMP TABLE orders(secret TEXT)"); err != nil {
					t.Fatal(err)
				}
			} else {
				other := tableReadFixture(t, "CREATE TABLE orders(secret TEXT)")
				if _, err := conn.ExecContext(context.Background(), "ATTACH DATABASE ? AS attached", other); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := conn.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inspectTableRead(context.Background(), tx, file, "main", "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128}); !errors.Is(err, errTableReadProfile) {
				t.Fatalf("native %s admitted: %v", kind, err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTableReadNativeEncodingAndDerivedCellBudget(t *testing.T) {
	for _, encoding := range []string{"UTF-16le", "UTF-16be"} {
		t.Run(encoding, func(t *testing.T) {
			file := tableReadFixture(t, "PRAGMA encoding='"+encoding+"';CREATE TABLE orders(a TEXT,b TEXT)")
			calls := 0
			deps := tableReadDependencies{open: sql.Open, limit: sqlite.Limit, session: func(tx *sql.Tx, opts dalgo2sql.DbOptions) (dal.ReadSession, error) {
				calls++
				return dalgo2sql.NewReadSession(tx, opts)
			}}
			if span, err := openTableRead(context.Background(), file, "main", "orders", TableReadLimits{MaxBytes: 1024, MaxColumns: 128}, deps); span != nil || err == nil || calls != 0 {
				t.Fatalf("unsupported encoding=%#v,%v,calls%d", span, err, calls)
			}
		})
	}
	for _, test := range []struct {
		name          string
		columns, size int
		deny          bool
	}{{"double-too-large", 2, 300, true}, {"double-within", 2, 250, false}, {"single-within", 1, 300, false}} {
		t.Run(test.name, func(t *testing.T) {
			columns, values := "a TEXT", "'"+strings.Repeat("a", test.size)+"'"
			if test.columns == 2 {
				columns += ",b TEXT"
				values += ",'" + strings.Repeat("b", test.size) + "'"
			}
			file := tableReadFixture(t, "CREATE TABLE orders("+columns+");INSERT INTO orders VALUES("+values+")")
			span := mustTableSpan(t, context.Background(), file, "orders", TableReadLimits{MaxBytes: 512, MaxColumns: 128})
			facts, err := span.Facts(context.Background())
			if err != nil || facts.Encoding != "UTF-8" || facts.MaxCellBytes != 512/uint32(test.columns) {
				t.Fatalf("facts=%#v,%v", facts, err)
			}
			q := dal.From(dal.NewQualifiedRootCollectionRef("main", "orders", "")).NewQuery().Limit(2).SelectIntoRecordset()
			reader, err := span.Session().ExecuteQueryToRecordsetReader(context.Background(), q)
			if err == nil {
				_, _, err = reader.Next()
				if closeErr := reader.Close(); closeErr != nil && !errors.Is(closeErr, err) {
					t.Fatal(closeErr)
				}
			}
			if (err != nil) != test.deny {
				t.Fatalf("deny=%v err=%v", test.deny, err)
			}
		})
	}
}

func TestTableReadNativeTextBytesRemainExact(t *testing.T) {
	file := tableReadFixture(t, `CREATE TABLE orders(a TEXT,b TEXT);INSERT INTO orders VALUES(CAST(x'610062' AS TEXT),CAST(x'fffe' AS TEXT))`)
	span := mustTableSpan(t, context.Background(), file, "orders", TableReadLimits{MaxBytes: 512, MaxColumns: 128})
	reader, err := span.Session().ExecuteQueryToRecordsetReader(context.Background(), dal.From(dal.NewQualifiedRootCollectionRef("main", "orders", "")).NewQuery().Limit(2).SelectIntoRecordset())
	if err != nil {
		t.Fatal(err)
	}
	row, rs, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"a\x00b", "\xff\xfe"} {
		got, err := row.GetValueByIndex(i, rs)
		if err != nil || got != want {
			t.Fatalf("native text bytes=%#v,%v want%#v", got, err, want)
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	// Invalid UTF8 is preserved by this driver. The host must refuse it before
	// serialization; the provider never silently replaces or truncates text.
}

func TestTableReadNativeShorterCallerDeadline(t *testing.T) {
	file := tableReadFixture(t, "CREATE TABLE orders(id INTEGER)")
	span := mustTableSpan(t, context.Background(), file, "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	reader, err := span.Session().ExecuteQueryToRecordsetReader(ctx, dal.NewTextQuery("WITH RECURSIVE counter(n) AS(VALUES(0) UNION ALL SELECT n+1 FROM counter) SELECT sum(n) FROM counter", nil))
	if reader != nil {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shorter caller deadline=%v", err)
	}
	if _, err := span.Facts(context.Background()); err != nil {
		t.Fatalf("caller timeout incorrectly renewed/destroyed original lifetime: %v", err)
	}
}

func TestTableReadNativeOriginalDeadlineBoundsIndependentCaller(t *testing.T) {
	file := tableReadFixture(t, "CREATE TABLE orders(id INTEGER)")
	original, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	span := mustTableSpan(t, original, file, "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128})
	held := span.Session()
	reader, err := held.ExecuteQueryToRecordsetReader(context.Background(), dal.NewTextQuery("WITH RECURSIVE counter(n) AS(VALUES(0) UNION ALL SELECT n+1 FROM counter) SELECT sum(n) FROM counter", nil))
	if reader != nil {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}
	// The span's deadline can cancel the linked query through AfterFunc just
	// before the child's equal-deadline timer fires; that native error is then
	// Canceled. Verify actual original-clock expiry and zero successful reader,
	// rather than depending on which equal-deadline cancellation wins.
	if reader != nil || (!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)) || !errors.Is(original.Err(), context.DeadlineExceeded) {
		t.Fatalf("original deadline escaped through independent caller: reader=%#v native=%v original=%v", reader, err, original.Err())
	}
	if _, err := span.Facts(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired original span remained live: %v", err)
	}
	if exists, err := held.Exists(context.Background(), nil); exists || err == nil {
		t.Fatal("held expired session returned success")
	}
}
