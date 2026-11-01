package dalgo2sqlite

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	dalrecord "github.com/dal-go/record"
)

// This concrete narrow wrapper preserves the caller's values/policies while
// binding every native call to the span's lifetime. It exposes no inner handle.
// Get/GetMulti can mutate caller-owned records before a concurrent cancellation;
// the error is authoritative, and no purge of those records is promised here.
type tableReadSession struct {
	owner *TableReadSpan
	inner dal.ReadSession
}

func (s *tableReadSession) callContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil || s.owner.closed.Load() {
		return nil, nil, errTableReadProfile
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := s.owner.ctx.Err(); err != nil {
		return nil, nil, err
	}
	var linked context.Context
	var cancel context.CancelFunc
	deadline, spanBound := s.owner.ctx.Deadline()
	callerDeadline, callerBound := ctx.Deadline()
	if spanBound && (!callerBound || deadline.Before(callerDeadline)) {
		linked, cancel = context.WithDeadline(ctx, deadline)
	} else {
		linked, cancel = context.WithCancel(ctx)
	}
	stop := context.AfterFunc(s.owner.ctx, cancel)
	release := func() { stop(); cancel() }
	if err := s.liveError(linked); err != nil {
		release()
		return nil, nil, err
	}
	return linked, release, nil
}

func (s *tableReadSession) liveError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.owner.ctx.Err(); err != nil {
		return err
	}
	if s.owner.closed.Load() {
		return errTableReadProfile
	}
	return nil
}

func (s *tableReadSession) Exists(ctx context.Context, key *dalrecord.Key) (bool, error) {
	linked, release, err := s.callContext(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	exists, err := s.inner.Exists(linked, key)
	if err != nil {
		return false, err
	}
	if err = s.liveError(linked); err != nil {
		return false, err
	}
	return exists, nil
}

func (s *tableReadSession) Get(ctx context.Context, record dalrecord.Record) error {
	linked, release, err := s.callContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err = s.inner.Get(linked, record); err != nil {
		return err
	}
	return s.liveError(linked)
}

func (s *tableReadSession) GetMulti(ctx context.Context, records []dalrecord.Record) error {
	linked, release, err := s.callContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err = s.inner.GetMulti(linked, records); err != nil {
		return err
	}
	return s.liveError(linked)
}

func (s *tableReadSession) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	linked, release, err := s.callContext(ctx)
	if err != nil {
		return nil, err
	}
	reader, err := s.inner.ExecuteQueryToRecordsReader(linked, query)
	if err != nil {
		release()
		if !nilTableReadValue(reader) {
			err = errors.Join(err, reader.Close())
		}
		return nil, err
	}
	if nilTableReadValue(reader) {
		release()
		return nil, errTableReadProfile
	}
	if err = s.liveError(linked); err != nil {
		release()
		return nil, errors.Join(err, reader.Close())
	}
	base := &tableReadReader{session: s, ctx: linked, release: release, closeNative: reader.Close}
	return &tableReadRecordsReader{base: base, reader: reader}, nil
}

func (s *tableReadSession) ExecuteQueryToRecordsetReader(ctx context.Context, query dal.Query, options ...recordset.Option) (dal.RecordsetReader, error) {
	linked, release, err := s.callContext(ctx)
	if err != nil {
		return nil, err
	}
	reader, err := s.inner.ExecuteQueryToRecordsetReader(linked, query, options...)
	if err != nil {
		release()
		if !nilTableReadValue(reader) {
			err = errors.Join(err, reader.Close())
		}
		return nil, err
	}
	if nilTableReadValue(reader) {
		release()
		return nil, errTableReadProfile
	}
	if err = s.liveError(linked); err != nil {
		release()
		return nil, errors.Join(err, reader.Close())
	}
	base := &tableReadReader{session: s, ctx: linked, release: release, closeNative: reader.Close}
	return &tableReadRecordsetReader{base: base, reader: reader}, nil
}

type tableReadCloseResult struct{ err error }
type tableReadReader struct {
	session     *tableReadSession
	ctx         context.Context
	release     context.CancelFunc
	closeNative func() error
	closed      atomic.Bool
	result      atomic.Pointer[tableReadCloseResult]
}

func (r *tableReadReader) liveError() error {
	if err := r.session.liveError(r.ctx); err != nil {
		return err
	}
	if r.closed.Load() {
		return errTableReadProfile
	}
	return nil
}

func (r *tableReadReader) Close() error {
	if r.closed.Swap(true) {
		if result := r.result.Load(); result != nil {
			return result.err
		}
		return nil
	}
	r.release()
	err := r.closeNative()
	r.result.Store(&tableReadCloseResult{err: err})
	return err
}

func (r *tableReadReader) cursor(native func() (string, error)) (string, error) {
	if err := r.liveError(); err != nil {
		return "", errors.Join(err, r.Close())
	}
	cursor, err := native()
	if err == nil {
		err = r.liveError()
	}
	if err != nil {
		return "", errors.Join(err, r.Close())
	}
	return cursor, nil
}

type tableReadRecordsReader struct {
	base   *tableReadReader
	reader dal.RecordsReader
}

func (r *tableReadRecordsReader) Close() error            { return r.base.Close() }
func (r *tableReadRecordsReader) Cursor() (string, error) { return r.base.cursor(r.reader.Cursor) }
func (r *tableReadRecordsReader) Next() (dalrecord.Record, error) {
	if err := r.base.liveError(); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	record, err := r.reader.Next()
	if err == nil {
		err = r.base.liveError()
	}
	if err != nil {
		return nil, errors.Join(err, r.Close())
	}
	return record, nil
}

type tableReadRecordsetReader struct {
	base   *tableReadReader
	reader dal.RecordsetReader
}

func (r *tableReadRecordsetReader) Close() error            { return r.base.Close() }
func (r *tableReadRecordsetReader) Cursor() (string, error) { return r.base.cursor(r.reader.Cursor) }
func (r *tableReadRecordsetReader) Recordset() recordset.Recordset {
	if r.base.liveError() != nil {
		return nil
	}
	return r.reader.Recordset()
}
func (r *tableReadRecordsetReader) Next() (recordset.Row, recordset.Recordset, error) {
	if err := r.base.liveError(); err != nil {
		return nil, nil, errors.Join(err, r.Close())
	}
	row, rs, err := r.reader.Next()
	if err == nil {
		err = r.base.liveError()
	}
	if err != nil {
		return nil, nil, errors.Join(err, r.Close())
	}
	return row, rs, nil
}
