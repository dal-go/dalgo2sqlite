package dalgo2sqlite

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	dalrecord "github.com/dal-go/record"
)

type tableSessionStub struct {
	dal.ReadSession
	exists    func(context.Context) (bool, error)
	get       func(context.Context) error
	multi     func(context.Context) error
	records   func(context.Context) (dal.RecordsReader, error)
	recordset func(context.Context) (dal.RecordsetReader, error)
}

func (s *tableSessionStub) Exists(ctx context.Context, _ *dalrecord.Key) (bool, error) {
	return s.exists(ctx)
}
func (s *tableSessionStub) Get(ctx context.Context, _ dalrecord.Record) error { return s.get(ctx) }
func (s *tableSessionStub) GetMulti(ctx context.Context, _ []dalrecord.Record) error {
	return s.multi(ctx)
}
func (s *tableSessionStub) ExecuteQueryToRecordsReader(ctx context.Context, _ dal.Query) (dal.RecordsReader, error) {
	return s.records(ctx)
}
func (s *tableSessionStub) ExecuteQueryToRecordsetReader(ctx context.Context, _ dal.Query, _ ...recordset.Option) (dal.RecordsetReader, error) {
	return s.recordset(ctx)
}

type tableRecordsStub struct {
	next   func() (dalrecord.Record, error)
	cursor func() (string, error)
	close  func() error
}

func (r *tableRecordsStub) Next() (dalrecord.Record, error) { return r.next() }
func (r *tableRecordsStub) Cursor() (string, error)         { return r.cursor() }
func (r *tableRecordsStub) Close() error                    { return r.close() }

type tableRecordsetStub struct {
	next   func() (recordset.Row, recordset.Recordset, error)
	cursor func() (string, error)
	close  func() error
	rs     recordset.Recordset
}

func (r *tableRecordsetStub) Next() (recordset.Row, recordset.Recordset, error) { return r.next() }
func (r *tableRecordsetStub) Cursor() (string, error)                           { return r.cursor() }
func (r *tableRecordsetStub) Close() error                                      { return r.close() }
func (r *tableRecordsetStub) Recordset() recordset.Recordset                    { return r.rs }

func tableSessionOwner(t *testing.T, ctx context.Context, inner dal.ReadSession) *tableReadSession {
	t.Helper()
	lifetime, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	return &tableReadSession{owner: &TableReadSpan{ctx: lifetime, cancel: cancel}, inner: inner}
}

func TestTableReadSessionScalarsRespectContextsAndErrors(t *testing.T) {
	boom := errors.New("original adapter failure")
	for _, method := range []string{"exists", "get", "multi"} {
		t.Run(method, func(t *testing.T) {
			for _, mode := range []string{"success", "adapter-error", "late-cancel", "late-close", "caller-cancelled", "span-cancelled", "closed", "nil-context"} {
				t.Run(mode, func(t *testing.T) {
					calls := 0
					stub := &tableSessionStub{}
					s := tableSessionOwner(t, context.Background(), stub)
					action := func(context.Context) error {
						calls++
						switch mode {
						case "adapter-error":
							return boom
						case "late-cancel":
							s.owner.cancel()
						case "late-close":
							s.owner.closed.Store(true)
						}
						return nil
					}
					stub.exists = func(ctx context.Context) (bool, error) { return true, action(ctx) }
					stub.get = action
					stub.multi = action
					caller := context.Background()
					if mode == "nil-context" {
						caller = tableReadNilContext(t)
					}
					if mode == "caller-cancelled" {
						ctx, cancel := context.WithCancel(caller)
						cancel()
						caller = ctx
					}
					if mode == "span-cancelled" {
						s.owner.cancel()
					}
					if mode == "closed" {
						s.owner.closed.Store(true)
					}
					var err error
					switch method {
					case "exists":
						var exists bool
						exists, err = s.Exists(caller, nil)
						if (err == nil) != exists {
							t.Fatal("failed Exists exposed successful true")
						}
					case "get":
						err = s.Get(caller, nil)
					case "multi":
						err = s.GetMulti(caller, nil)
					}
					if (err == nil) != (mode == "success") {
						t.Fatalf("mode=%s err=%v", mode, err)
					}
					if mode == "adapter-error" && !errors.Is(err, boom) {
						t.Fatal("original adapter error lost")
					}
					wantCalls := 1
					switch mode {
					case "caller-cancelled", "span-cancelled", "closed", "nil-context":
						wantCalls = 0
					}
					if calls != wantCalls {
						t.Fatalf("calls=%d,want%d", calls, wantCalls)
					}
				})
			}
		})
	}
}

type tableContextValueKey struct{}

func TestTableReadSessionDeadlineAndPolicyContext(t *testing.T) {
	original := time.Now().Add(time.Hour)
	for _, shorterCaller := range []bool{false, true} {
		t.Run(map[bool]string{false: "span-earlier", true: "caller-earlier"}[shorterCaller], func(t *testing.T) {
			originalCtx, cancel := context.WithDeadline(context.Background(), original)
			defer cancel()
			wanted := original
			caller := context.WithValue(context.Background(), tableContextValueKey{}, "policy-snapshot")
			if shorterCaller {
				wanted = time.Now().Add(time.Minute)
				var stop context.CancelFunc
				caller, stop = context.WithDeadline(caller, wanted)
				defer stop()
			}
			var saved context.Context
			stub := &tableSessionStub{get: func(ctx context.Context) error {
				saved = ctx
				deadline, ok := ctx.Deadline()
				if !ok || !deadline.Equal(wanted) || ctx.Value(tableContextValueKey{}) != "policy-snapshot" {
					t.Fatal("lost original deadline or caller policy context")
				}
				return nil
			}}
			s := tableSessionOwner(t, originalCtx, stub)
			if err := s.Get(caller, nil); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(saved.Err(), context.Canceled) {
				t.Fatal("scalar call retained linked context")
			}
		})
	}
}

type tableBlockingDeadline struct {
	context.Context
	once             sync.Once
	entered, release chan struct{}
}

func (c *tableBlockingDeadline) Deadline() (time.Time, bool) {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Context.Deadline()
}
func TestTableReadSessionCancellationWhileLinkingCallsZeroAdapter(t *testing.T) {
	stub := &tableSessionStub{get: func(context.Context) error { t.Error("cancelled link reached adapter"); return nil }}
	s := tableSessionOwner(t, context.Background(), stub)
	caller := &tableBlockingDeadline{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- s.Get(caller, nil) }()
	<-caller.entered
	s.owner.cancel()
	close(caller.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("link cancellation=%v", err)
	}
}

func TestTableReadSessionRefusesMalformedAndPartialReaders(t *testing.T) {
	boom := errors.New("reader opening error")
	cleanup := errors.New("partial reader cleanup error")
	for _, recordsetMode := range []bool{false, true} {
		for _, mode := range []string{"nil-success", "typednil-success", "nil-error", "typednil-error", "partial-error", "late-cancel", "closed-before-open"} {
			t.Run(map[bool]string{false: "records-", true: "recordset-"}[recordsetMode]+mode, func(t *testing.T) {
				calls, closes := 0, 0
				stub := &tableSessionStub{}
				s := tableSessionOwner(t, context.Background(), stub)
				closeNative := func() error { closes++; return cleanup }
				selectError := func() error {
					if mode == "nil-error" || mode == "typednil-error" || mode == "partial-error" {
						return boom
					}
					return nil
				}
				stub.records = func(context.Context) (dal.RecordsReader, error) {
					calls++
					if mode == "late-cancel" {
						s.owner.cancel()
					}
					if mode == "nil-success" || mode == "nil-error" {
						return nil, selectError()
					}
					if mode == "typednil-success" || mode == "typednil-error" {
						var reader *tableRecordsStub
						return reader, selectError()
					}
					return &tableRecordsStub{close: closeNative}, selectError()
				}
				stub.recordset = func(context.Context) (dal.RecordsetReader, error) {
					calls++
					if mode == "late-cancel" {
						s.owner.cancel()
					}
					if mode == "nil-success" || mode == "nil-error" {
						return nil, selectError()
					}
					if mode == "typednil-success" || mode == "typednil-error" {
						var reader *tableRecordsetStub
						return reader, selectError()
					}
					return &tableRecordsetStub{close: closeNative}, selectError()
				}
				if mode == "closed-before-open" {
					s.owner.closed.Store(true)
				}
				var err error
				if recordsetMode {
					var reader dal.RecordsetReader
					reader, err = s.ExecuteQueryToRecordsetReader(context.Background(), nil)
					if reader != nil {
						t.Fatal("malformed recordset reader published")
					}
				} else {
					var reader dal.RecordsReader
					reader, err = s.ExecuteQueryToRecordsReader(context.Background(), nil)
					if reader != nil {
						t.Fatal("malformed records reader published")
					}
				}
				if err == nil {
					t.Fatal("malformed/closed reader succeeded")
				}
				wantCloses := 0
				if mode == "partial-error" || mode == "late-cancel" {
					wantCloses = 1
				}
				if closes != wantCloses {
					t.Fatalf("closes=%d,want%d", closes, wantCloses)
				}
				if mode == "partial-error" && (!errors.Is(err, boom) || !errors.Is(err, cleanup)) {
					t.Fatal("opening/cleanup error lost")
				}
				if mode == "closed-before-open" && calls != 0 {
					t.Fatal("closed reader reached adapter")
				}
			})
		}
	}
}

func TestTableReadSessionReadersOwnContextThroughEOFCloseAndLateValues(t *testing.T) {
	cleanup := errors.New("observed reader cleanup failure")
	for _, recordsetMode := range []bool{false, true} {
		for _, ending := range []string{"eof", "late-cancel", "close-before-next", "cursor-late-cancel", "cursor-error"} {
			t.Run(map[bool]string{false: "records-", true: "recordset-"}[recordsetMode]+ending, func(t *testing.T) {
				rs := recordset.NewColumnarRecordset("orders", recordset.NewColumn("id", int64(0)))
				row := rs.NewRow()
				record := dalrecord.NewRecordWithData(dalrecord.NewKeyWithID("orders", "one"), map[string]any{"id": int64(1)})
				stub := &tableSessionStub{}
				s := tableSessionOwner(t, context.Background(), stub)
				var linked context.Context
				closes := 0
				closeNative := func() error {
					closes++
					if linked.Err() == nil {
						t.Error("reader Close failed to cancel native context before I/O")
					}
					return cleanup
				}
				cursor := func() (string, error) {
					if ending == "cursor-late-cancel" {
						s.owner.cancel()
					}
					if ending == "cursor-error" {
						return "private-cursor", cleanup
					}
					return "cursor", nil
				}
				count := 0
				nextError := func() error {
					count++
					if ending == "late-cancel" {
						s.owner.cancel()
						return nil
					}
					if count == 2 {
						return dal.ErrNoMoreRecords
					}
					return nil
				}
				stub.records = func(ctx context.Context) (dal.RecordsReader, error) {
					linked = ctx
					return &tableRecordsStub{next: func() (dalrecord.Record, error) { return record, nextError() }, cursor: cursor, close: closeNative}, nil
				}
				stub.recordset = func(ctx context.Context) (dal.RecordsetReader, error) {
					linked = ctx
					return &tableRecordsetStub{next: func() (recordset.Row, recordset.Recordset, error) { return row, rs, nextError() }, cursor: cursor, close: closeNative, rs: rs}, nil
				}
				var reader dal.Reader
				var next func() error
				if recordsetMode {
					r, err := s.ExecuteQueryToRecordsetReader(context.Background(), nil)
					if err != nil {
						t.Fatal(err)
					}
					reader = r
					if r.Recordset() != rs {
						t.Fatal("live metadata absent")
					}
					next = func() error {
						got, gotRS, err := r.Next()
						if err != nil && (got != nil || gotRS != nil) {
							t.Fatal("late native row escaped")
						}
						return err
					}
				} else {
					r, err := s.ExecuteQueryToRecordsReader(context.Background(), nil)
					if err != nil {
						t.Fatal(err)
					}
					reader = r
					next = func() error {
						got, err := r.Next()
						if err != nil && got != nil {
							t.Fatal("late native record escaped")
						}
						return err
					}
				}
				if linked.Err() != nil {
					t.Fatal("reader context released before consumption")
				}
				switch ending {
				case "eof":
					if cursor, err := reader.Cursor(); cursor != "cursor" || err != nil {
						t.Fatalf("cursor=%q,%v", cursor, err)
					}
					if err := next(); err != nil {
						t.Fatal(err)
					}
					if err := next(); !errors.Is(err, dal.ErrNoMoreRecords) || !errors.Is(err, cleanup) {
						t.Fatalf("EOF lost Close failure: %v", err)
					}
				case "late-cancel":
					if err := next(); !errors.Is(err, context.Canceled) {
						t.Fatalf("late value=%v", err)
					}
				case "close-before-next":
					if err := reader.Close(); !errors.Is(err, cleanup) {
						t.Fatal(err)
					}
					if err := next(); err == nil {
						t.Fatal("closed Next succeeded")
					}
				case "cursor-late-cancel", "cursor-error":
					if value, err := reader.Cursor(); value != "" || err == nil {
						t.Fatalf("failed cursor escaped=%q,%v", value, err)
					}
				}
				if err := reader.Close(); !errors.Is(err, cleanup) {
					t.Fatalf("cached reader close error lost: %v", err)
				}
				if cursor, err := reader.Cursor(); cursor != "" || err == nil {
					t.Fatal("closed cursor escaped")
				}
				if closes != 1 {
					t.Fatalf("native Close count=%d", closes)
				}
				if recordsetMode && reader.(dal.RecordsetReader).Recordset() != nil {
					t.Fatal("closed driver metadata exposed")
				}
			})
		}
	}
}

func TestTableReadReaderCloseDoesNotWaitForAnotherNativeClose(t *testing.T) {
	s := tableSessionOwner(t, context.Background(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	boom := errors.New("native close failure")
	var count atomic.Int32
	reader := &tableReadReader{session: s, ctx: ctx, release: cancel, closeNative: func() error { count.Add(1); close(entered); <-release; return boom }}
	done := make(chan error, 1)
	go func() { done <- reader.Close() }()
	<-entered
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.liveError(); err == nil {
		t.Fatal("Close left reader live")
	}
	close(release)
	if err := <-done; !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := reader.Close(); !errors.Is(err, boom) || count.Load() != 1 {
		t.Fatal("native Close duplicated or observed error lost")
	}
	// Invalidation itself also refuses while cancellation has not yet run.
	active := &tableReadReader{session: s, ctx: context.Background()}
	active.closed.Store(true)
	if active.liveError() == nil {
		t.Fatal("invalidated reader remained live")
	}
}

func TestTableReadCloseCancelsIndependentReadContext(t *testing.T) {
	file := tableReadFixture(t, "CREATE TABLE orders(id INTEGER)")
	span := mustTableSpan(t, context.Background(), file, "orders", TableReadLimits{MaxBytes: 256 * 1024, MaxColumns: 128})
	queryCtx, queryCancel := context.WithCancel(context.Background())
	defer queryCancel()
	session := span.Session()
	started := make(chan struct{})
	queryDone := make(chan error, 1)
	go func() {
		close(started)
		reader, err := session.ExecuteQueryToRecordsetReader(queryCtx, dal.NewTextQuery("WITH RECURSIVE counter(n) AS(VALUES(0) UNION ALL SELECT n+1 FROM counter) SELECT sum(n) FROM counter", nil))
		if reader != nil {
			_ = reader.Close()
		}
		queryDone <- err
	}()
	<-started
	time.Sleep(30 * time.Millisecond)
	closeDone := make(chan error, 1)
	go func() { closeDone <- span.Close() }()
	stalled := false
	select {
	case err := <-closeDone:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(150 * time.Millisecond):
		stalled = true
	}
	queryCancel()
	select {
	case <-queryDone:
	case <-time.After(2 * time.Second):
		t.Fatal("owned query cleanup stalled")
	}
	if stalled {
		select {
		case <-closeDone:
		case <-time.After(time.Second):
			t.Fatal("owned Close cleanup stalled")
		}
		t.Fatal("span.Close did not cancel native operation supplied an independent caller context")
	}
}

func tableReadNilContext(t *testing.T) context.Context {
	t.Helper()
	return nil
}
