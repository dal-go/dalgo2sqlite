package dalgo2sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"modernc.org/sqlite"
)

// openClassifierTestDB opens an in-memory modernc.org/sqlite database with a
// table exercising all three constraint kinds this file's tests need:
//   - id is a duplicate-primary-key case (SQLITE_CONSTRAINT_PRIMARYKEY)
//   - email is a duplicate-unique-index case (SQLITE_CONSTRAINT_UNIQUE)
//   - required is a NOT NULL case, and score has a CHECK — both are
//     constraint violations that are NOT duplicate keys.
func openClassifierTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const ddl = `CREATE TABLE t (
		id       TEXT PRIMARY KEY,
		email    TEXT UNIQUE,
		required TEXT NOT NULL,
		score    INTEGER CHECK (score >= 0)
	)`
	if _, err := db.ExecContext(context.Background(), ddl); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	return db
}

// TestIsAlreadyExists_DuplicatePrimaryKey proves a duplicate primary key —
// SQLITE_CONSTRAINT_PRIMARYKEY (1555) — is classified as already-exists.
// This is the case the shared dalgotest conformance suite's unconditional
// "rejects an Insert over an existing key" check exercises, and the case a
// classifier matching only SQLITE_CONSTRAINT_UNIQUE (2067) would miss.
func TestIsAlreadyExists_DuplicatePrimaryKey(t *testing.T) {
	db := openClassifierTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO t (id, required) VALUES (?, ?)`, "dup", "x"); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO t (id, required) VALUES (?, ?)`, "dup", "y")
	if err == nil {
		t.Fatal("expected a duplicate primary key error, got nil")
	}

	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("expected a *sqlite.Error in the chain, got: %v (%T)", err, err)
	}
	if sqliteErr.Code() != sqliteConstraintPrimaryKey {
		t.Fatalf("expected extended code %d (SQLITE_CONSTRAINT_PRIMARYKEY), got %d: %v",
			sqliteConstraintPrimaryKey, sqliteErr.Code(), err)
	}

	if !IsAlreadyExists(err) {
		t.Errorf("IsAlreadyExists(%v) = false, want true for a duplicate primary key", err)
	}
}

// TestIsAlreadyExists_DuplicateUniqueIndex proves a duplicate value in a
// UNIQUE (non-primary-key) index — SQLITE_CONSTRAINT_UNIQUE (2067) — is
// also classified as already-exists.
func TestIsAlreadyExists_DuplicateUniqueIndex(t *testing.T) {
	db := openClassifierTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO t (id, email, required) VALUES (?, ?, ?)`,
		"id1", "same@example.com", "x"); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO t (id, email, required) VALUES (?, ?, ?)`,
		"id2", "same@example.com", "y")
	if err == nil {
		t.Fatal("expected a duplicate unique-index error, got nil")
	}

	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("expected a *sqlite.Error in the chain, got: %v (%T)", err, err)
	}
	if sqliteErr.Code() != sqliteConstraintUnique {
		t.Fatalf("expected extended code %d (SQLITE_CONSTRAINT_UNIQUE), got %d: %v",
			sqliteConstraintUnique, sqliteErr.Code(), err)
	}

	if !IsAlreadyExists(err) {
		t.Errorf("IsAlreadyExists(%v) = false, want true for a duplicate unique-index value", err)
	}
}

// TestIsAlreadyExists_NotNullViolation_NotClassified proves a NOT NULL
// violation — which reports the primary SQLITE_CONSTRAINT (19) rather than
// a duplicate-key extended code — is NOT classified as already-exists.
// This is the failure mode a classifier matching the primary result code
// (rather than the extended one) would misclassify.
func TestIsAlreadyExists_NotNullViolation_NotClassified(t *testing.T) {
	db := openClassifierTestDB(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `INSERT INTO t (id, required) VALUES (?, ?)`, "id1", nil)
	if err == nil {
		t.Fatal("expected a NOT NULL constraint error, got nil")
	}

	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("expected a *sqlite.Error in the chain, got: %v (%T)", err, err)
	}
	if sqliteErr.Code() == sqliteConstraintPrimaryKey || sqliteErr.Code() == sqliteConstraintUnique {
		t.Fatalf("test setup bug: NOT NULL insert reported a duplicate-key code %d: %v", sqliteErr.Code(), err)
	}

	if IsAlreadyExists(err) {
		t.Errorf("IsAlreadyExists(%v) = true, want false for a NOT NULL violation", err)
	}
}

// TestIsAlreadyExists_CheckViolation_NotClassified proves a CHECK
// violation is likewise not classified as already-exists.
func TestIsAlreadyExists_CheckViolation_NotClassified(t *testing.T) {
	db := openClassifierTestDB(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `INSERT INTO t (id, required, score) VALUES (?, ?, ?)`, "id1", "x", -1)
	if err == nil {
		t.Fatal("expected a CHECK constraint error, got nil")
	}

	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("expected a *sqlite.Error in the chain, got: %v (%T)", err, err)
	}
	if sqliteErr.Code() == sqliteConstraintPrimaryKey || sqliteErr.Code() == sqliteConstraintUnique {
		t.Fatalf("test setup bug: CHECK insert reported a duplicate-key code %d: %v", sqliteErr.Code(), err)
	}

	if IsAlreadyExists(err) {
		t.Errorf("IsAlreadyExists(%v) = true, want false for a CHECK violation", err)
	}
}

// TestIsAlreadyExists_NonSqliteError proves a plain, non-*sqlite.Error error
// (nothing in the chain) is not classified either.
func TestIsAlreadyExists_NonSqliteError(t *testing.T) {
	if IsAlreadyExists(errors.New("boom")) {
		t.Error("IsAlreadyExists(plain error) = true, want false")
	}
	if IsAlreadyExists(nil) {
		t.Error("IsAlreadyExists(nil) = true, want false")
	}
}
