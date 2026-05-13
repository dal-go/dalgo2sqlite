# Feature: dbschema + ddl + ConcurrencyAware Coverage for SQLite

**Status:** Approved
**Source Idea:** —
**Date:** 2026-05-13
**Owner:** alex

## Summary

`dalgo2sqlite` is a new dal-go driver dedicated to SQLite. It composes [`dalgo2sql`](https://github.com/dal-go/dalgo2sql) for the `dal.DB` read/write surface (`InsertMulti`, `Get`, `Set`, `Delete`, transactions, recordset reader) and adds **SQLite-native implementations** of the three schema-modification capabilities shipped in `dal-go/dalgo`:

- **`dbschema.SchemaReader`** — schema introspection via `sqlite_master` and `pragma_table_info`
- **`ddl.SchemaModifier`** — SQLite-flavored `CreateCollection` / `DropCollection` / `AlterCollection`
- **`dal.ConcurrencyAware`** — advertises `SupportsConcurrentConnections() = false` (SQLite is single-writer)

A new `dalgo2sqlite.NewDatabase(dbPath string)` constructor opens the SQLite file via `sql.Open("sqlite3", dbPath)` (mattn driver) and returns a `dal.DB` that satisfies all four interfaces.

This Feature is the SQLite half of the SQL-backend dbschema/ddl coverage. PostgreSQL is a separate follow-up Feature (likely a future `dalgo2pg` repo using the same pattern).

## Synopsis

```go
import (
    "github.com/dal-go/dalgo/dal"
    "github.com/dal-go/dalgo/dbschema"
    "github.com/dal-go/dalgo/ddl"
    "github.com/dal-go/dalgo2sqlite"
)

db, err := dalgo2sqlite.NewDatabase("./chinook.db")
// err handling…
defer db.Close()

// dal.DB surface (delegated to inner dalgo2sql)
_ = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error { /*…*/ })

// dbschema introspection
reader := db.(dbschema.SchemaReader)
tables, _ := reader.ListCollections(ctx)
def, _ := reader.DescribeCollection(ctx, "users")

// ddl
_ = ddl.CreateCollection(ctx, db, def)             // dispatches through SchemaModifier
_ = ddl.AlterCollection(ctx, db, "users", ddl.AddField(/*…*/))

// concurrency hint
parallel := db.(dal.ConcurrencyAware).SupportsConcurrentConnections() // false for SQLite
```

## Problem

The `dal-go/dalgo` package shipped `dbschema.SchemaReader`, `ddl.SchemaModifier`, and `dal.ConcurrencyAware` as engine-agnostic interfaces. Consumers (e.g. `datatug-cli`'s `db copy`) need at least one SQL-engine driver to wire them through to real SQL so cross-engine workflows are testable end-to-end. `dalgo2sql` is intentionally engine-agnostic (a `*sql.DB` wrapper for any registered driver) and SHOULD NOT carry SQLite-specific SQL. The right home is a new SQLite-dedicated package.

## Behavior

### Construction

#### REQ: new-database-constructor

The package MUST export `dalgo2sqlite.NewDatabase(dbPath string) (dal.DB, error)`. The constructor opens the SQLite file via `sql.Open("sqlite3", dbPath)`, wraps it via `dalgo2sql.NewDatabase`, and returns a `dal.DB` that satisfies (in addition to the base `dal.DB` contract): `dbschema.SchemaReader`, `ddl.SchemaModifier`, and `dal.ConcurrencyAware`.

`NewDatabase` MUST surface `sql.Open` errors verbatim (wrapped with `fmt.Errorf` and `%w`). The constructor MUST NOT execute any DDL or write any data — only open the file (which SQLite creates on first connection if absent).

#### REQ: ping-on-open

Before returning, `NewDatabase` MUST call `(*sql.DB).PingContext(ctx)` (with a `context.Background()` for the synchronous constructor) to detect malformed-file / permission errors at construction time rather than at first operation. A ping failure MUST return `(nil, error)` and close the underlying `*sql.DB`.

### Composition with dalgo2sql

#### REQ: dalgo2sql-delegation

The `dalgo2sqlite.Database` type MUST hold a `dalgo2sql.Database` as a private field (composition, not embedding) and delegate every `dal.DB` method to that inner database EXCEPT `SupportsConcurrentConnections()`. The composition pattern (rather than embedding) is required because `dalgo2sql.Database` may not yet implement `dal.ConcurrencyAware` and `dalgo2sqlite.Database` MUST control its own answer to that question.

#### REQ: no-fork-of-dalgo2sql

This Feature MUST NOT modify the `dalgo2sql` package. `dalgo2sql` remains the engine-agnostic SQL driver; SQLite-specific behavior lives exclusively in `dalgo2sqlite`. Any bug found in `dalgo2sql` during this work is reported separately and out of this Feature's scope.

### ConcurrencyAware

#### REQ: concurrency-aware-false

`Database.SupportsConcurrentConnections() bool` MUST return `false`. SQLite serializes writers; concurrent writers deadlock or fail with `database is locked`. WAL mode permits concurrent readers, but the dalgo `ConcurrencyAware` contract intentionally collapses to a single boolean (per `dal.ConcurrencyAware`'s godoc: "A driver like SQLite that supports concurrent readers but serializes writers collapses to false."). The MVP returns `false` unconditionally.

### dbschema.SchemaReader

The `dbschema.SchemaReader` interface in `dal-go/dalgo` (file `dbschema/reader.go`) is the canonical contract. It declares **five** required methods: `ListCollections`, `DescribeCollection`, `ListIndexes`, `ListConstraints`, `ListReferrers`. Interface satisfaction is structural — all five must be present. SQLite has no concept of foreign-key referrers in a queryable form beyond `PRAGMA foreign_key_list`, and constraints are limited to NOT NULL, CHECK, and FOREIGN KEY — so `ListConstraints` is implemented and `ListReferrers` is implemented as a best-effort survey via `PRAGMA foreign_key_list` across all tables.

#### REQ: list-collections

`Database.ListCollections(ctx context.Context, parent *dal.Key) ([]dal.CollectionRef, error)` MUST return the user-defined tables in the database. The `parent *dal.Key` argument is ignored (SQLite has no catalog/schema hierarchy) — when non-nil it is treated as "everything visible" (same as nil). The result MUST come from `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name` and be returned as a slice of `dal.CollectionRef` whose `Name` field is the table name. SQLite's internal tables (`sqlite_sequence`, `sqlite_stat1`, etc.) MUST be excluded.

#### REQ: describe-collection

`Database.DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error)` MUST return the full `*CollectionDef` for `ref.Name`, populated as follows:

- `Name`: the collection name (echoed from `ref.Name`)
- `Fields`: ordered list of `dbschema.FieldDef`, one per column, in `pragma_table_info` column-order. Each field's `Type` is mapped from the SQLite-affinity per the table in REQ:type-mapping; `Nullable` reflects the `notnull` column from `pragma_table_info` (inverted); `Default` reflects the `dflt_value` column when non-NULL; `AutoIncrement` is `true` for the column declared as `INTEGER PRIMARY KEY AUTOINCREMENT` and `false` otherwise.
- `PrimaryKey`: ordered list of column names where `pragma_table_info.pk > 0`, sorted by the `pk` value (1, 2, …) — i.e. composite keys preserve their declared order.
- `Indexes`: list of `dbschema.IndexDef` from `pragma_index_list(<table>)`, EXCLUDING the implicit auto-created index for `INTEGER PRIMARY KEY` (its `origin` is `'pk'`). For each non-pk index, fields come from `pragma_index_info(<index_name>)`.

If the collection does not exist, `DescribeCollection` MUST return `(nil, err)` where `err` is a typed sentinel whose `Error()` message contains the substring `"not found"` and the collection name. The exact error type is plan-time: if `dal-go/dalgo/dbschema` adds a canonical `NotFoundError` after this Feature is specified, the driver uses it; otherwise the driver defines its own `dalgo2sqlite.ErrCollectionNotFound` and documents the contract. See Outstanding Questions.

#### REQ: list-indexes

`Database.ListIndexes(ctx, ref *dal.CollectionRef) ([]dbschema.IndexDef, error)` MUST return the same index list as `DescribeCollection(ctx, ref).Indexes` (i.e. user-defined indexes, excluding the implicit PK index). This duplication is intentional per the `SchemaReader` godoc: "ListIndexes returns the indexes on a collection. The returned slice MAY include indexes already reported inline via DescribeCollection's Indexes field."

#### REQ: list-constraints

`Database.ListConstraints(ctx, ref *dal.CollectionRef) ([]dbschema.ConstraintDef, error)` MUST return a best-effort survey of constraints declared on the table:
- The primary-key constraint (mirroring `DescribeCollection.PrimaryKey`)
- Foreign-key constraints from `PRAGMA foreign_key_list(<table>)`
- NOT NULL declarations and CHECK clauses are NOT enumerated by this method (SQLite's introspection doesn't expose CHECK clause source SQL portably); calling code that needs them reads them from `DescribeCollection.Fields`.

#### REQ: list-referrers

`Database.ListReferrers(ctx, ref *dal.CollectionRef) ([]dbschema.Referrer, error)` MUST scan every table via `ListCollections` then query `PRAGMA foreign_key_list(<other>)` for each, returning the tables whose FK rows reference `ref.Name`. This is an O(N) operation where N is the number of tables. Acceptable for MVP given SQLite's small typical scale; callers needing efficiency at scale should consider a follow-up index-table approach.

### Type mapping (FieldDef.Type ↔ SQLite native)

#### REQ: type-mapping

The driver MUST implement a bidirectional translation between `dbschema.Type` (the 8-variant enum: `Null`, `Bool`, `Int`, `Float`, `String`, `Bytes`, `Time`, `Decimal`) and SQLite native column types:

| `dbschema.Type` | SQLite native (emitted by ddl) | SQLite affinity recognized (by dbschema reader) |
|---|---|---|
| `Bool` | `INTEGER` | `INTEGER` with `CHECK (col IN (0,1))` OR a column named with bool-typical suffix (driver-internal heuristic) |
| `Int` | `INTEGER` | `INTEGER` |
| `Float` | `REAL` | `REAL` |
| `String` | `TEXT` (length hint silently ignored — SQLite ignores VARCHAR(N) length) | `TEXT` |
| `Bytes` | `BLOB` | `BLOB` |
| `Time` | `TEXT` (ISO 8601 storage) | `TEXT` AND a per-column marker the driver writes during CreateCollection (e.g. column comment if SQLite supports one; else a separate `_meta` table — plan-time choice) |
| `Decimal` | `NUMERIC` | `NUMERIC` |
| `Null` | rejected (cannot create a column with type Null) | n/a |

The Time mapping is the noteworthy one: SQLite has no native datetime type. Plan-time picks ONE round-trippable storage form and the dbschema reader recognizes it. Other storage forms (Unix epoch in `INTEGER`) are rejected for round-trip fidelity.

### ddl.SchemaModifier

#### REQ: create-collection

`Database.CreateCollection(ctx, c dbschema.CollectionDef, opts ...ddl.Option) error` MUST generate and execute a `CREATE TABLE` statement reflecting `c.Fields`, `c.PrimaryKey`, and emit a `CREATE INDEX` for each entry in `c.Indexes`. The order is `CREATE TABLE` first, then each `CREATE INDEX`. All operations execute inside a single SQLite transaction (per REQ:transactional-ddl).

When `ddl.WithIfNotExists()` is supplied in opts, the generated SQL uses `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS`.

A `c.Fields` entry with `Type = dbschema.Null` MUST be rejected before any SQL execution with a clear error naming the offending field.

#### REQ: drop-collection

`Database.DropCollection(ctx, name string, opts ...ddl.Option) error` MUST execute `DROP TABLE <name>`. When `ddl.WithIfExists()` is supplied, the SQL becomes `DROP TABLE IF EXISTS <name>`. SQLite's `DROP TABLE` automatically drops the table's indexes; no separate `DROP INDEX` calls are needed.

#### REQ: alter-collection

`Database.AlterCollection(ctx, name string, ops ...ddl.AlterOp) error` MUST support the six AlterOps shipped in `dal-go/dalgo/ddl`:

- `AddField(f)` → `ALTER TABLE <name> ADD COLUMN <f.Name> <type>`
- `DropField(n)` → `ALTER TABLE <name> DROP COLUMN <n>` (SQLite ≥ 3.35.0)
- `RenameField(old, new)` → `ALTER TABLE <name> RENAME COLUMN <old> TO <new>` (SQLite ≥ 3.25.0)
- `ModifyField(n, newDef)` → SQLite has no direct `ALTER COLUMN`. Implementation does the standard SQLite migration dance: `CREATE TABLE <name>_new …; INSERT INTO <name>_new SELECT … FROM <name>; DROP TABLE <name>; ALTER TABLE <name>_new RENAME TO <name>;` — all inside a transaction per REQ:transactional-ddl.
- `AddIndex(idx)` → `CREATE INDEX <idx.Name> ON <name> (<columns>)`
- `DropIndex(n)` → `DROP INDEX <n>`

Each op MUST be applied in the order supplied. The whole `AlterCollection` call runs inside a single transaction (per REQ:transactional-ddl).

#### REQ: transactional-ddl

`Database` MUST implement the `ddl.TransactionalDDL` capability (or the equivalent marker interface defined in `dal-go/dalgo/ddl/`). All multi-statement DDL operations (`CreateCollection` with indexes, `AlterCollection` with multiple ops, `ModifyField`'s migration dance) MUST execute inside a single `BEGIN…COMMIT` transaction. On any error, the implementation MUST `ROLLBACK` and return the error — no partial-success state on the database.

### Round-trip fidelity

#### REQ: create-describe-round-trip

For every `dbschema.CollectionDef` value `c` whose `Fields` contain only the supported types (per REQ:type-mapping), invoking `CreateCollection(ctx, c)` followed by `DescribeCollection(ctx, c.Name)` MUST return a `CollectionDef` semantically equal to `c`:

- Field names, order, types, nullability, default expressions, `AutoIncrement` markers MUST round-trip exactly
- Primary key columns and order MUST round-trip exactly
- Indexes (name, columns, unique flag) MUST round-trip exactly

Type-mapping lossiness is the one explicit exception: `String` fields with a `Length` hint round-trip to `String` without the hint (SQLite ignores length). `Time` fields round-trip provided the driver-internal marker per REQ:type-mapping is preserved.

## Architecture

### Files (target layout)

| File | Responsibility |
|---|---|
| `database.go` | `Database` struct (composition over `dalgo2sql.Database`), `NewDatabase` constructor, `SupportsConcurrentConnections()` returning `false`. Delegating `dal.DB` methods. |
| `schema_reader.go` | `ListCollections` + `DescribeCollection` impls; queries `sqlite_master` and `pragma_table_info` / `pragma_index_list` / `pragma_index_info`. |
| `schema_modifier.go` | `CreateCollection` / `DropCollection` / `AlterCollection` impls; transactional execution. |
| `type_mapping.go` | The `dbschema.Type` ↔ SQLite native string mapping table + `Time` storage marker logic. |
| `sql_gen.go` | Pure functions that build the actual SQL strings from `dbschema.CollectionDef` / `AlterOp` values. Isolated from the I/O layer for unit-testability. |
| `database_test.go`, `schema_reader_test.go`, `schema_modifier_test.go`, `type_mapping_test.go`, `sql_gen_test.go` | Tests, one per source file. |
| `end2end/sqlite_e2e_test.go` | End-to-end test against a real SQLite file (via `mattn/go-sqlite3`) exercising the round-trip REQ. |

### Dependencies

- `github.com/dal-go/dalgo` (latest with `dbschema`, `ddl`, `ConcurrencyAware`). Pin in `go.mod` after the Feature is plan-time-implemented.
- `github.com/dal-go/dalgo2sql` (for the `dal.DB` surface composition). Pin as a direct require.
- `github.com/mattn/go-sqlite3` (the SQLite driver registered via `_ "github.com/mattn/go-sqlite3"` import).
- `github.com/DATA-DOG/go-sqlmock` (test only — already a transitive dependency through dalgo2sql).

## Testing Strategy

- Unit tests for `sql_gen.go` (pure SQL-string generation, no I/O — straightforward table-driven cases).
- Unit tests for `type_mapping.go` (round-trip table, also pure).
- Mock-driven tests for `schema_reader.go` and `schema_modifier.go` via `go-sqlmock`.
- One end-to-end test against a real SQLite file under `end2end/` exercising REQ:create-describe-round-trip across the Chinook schema (or a representative subset thereof).

## Rehearse Integration

All ACs are testable via `go test ./...`. SQLite is a single-binary embedded engine, so end-to-end tests run anywhere Go runs; no external infrastructure required.

## Out of Scope

- **PostgreSQL.** Separate follow-up Feature (likely a future `dalgo2pg` repo).
- **MySQL, MS SQL Server, other engines.** Future Features per engine.
- **Modifying `dalgo2sql`.** Out of scope per user instruction; this Feature creates a new repo wrapping `dalgo2sql`.
- **Per-column comments / metadata that SQLite doesn't natively support.** If SQLite's column-comment surface is too thin for the `Time` marker, the driver picks an alternate storage strategy (e.g. a sidecar `_dalgo_meta` table). The exact choice is plan-time; this REQ list pins only that round-trip MUST work, not the mechanism.
- **Read-only `Adapter` for legacy SQLite files without driver-written metadata.** The dbschema reader assumes either the file was created by this driver OR all columns map cleanly via the SQLite-affinity heuristic. Files with bespoke column types (`MY_CUSTOM_TYPE`) produce an explicit `*dbschema.NotSupportedError` at `DescribeCollection` time.
- **Performance work** — no bulk-load tuning, no prepared-statement caching, no PRAGMA tweaks. Correctness via the standard `database/sql` API is the MVP bar.

## Assumption Carryover

No source Idea exists. The implicit assumptions this Feature commits to:

| Tier | Assumption | Status |
|------|------------|--------|
| Must-be-true | `dalgo2sql.Database` can be composed without modification — its public API exposes everything a wrapping struct needs to delegate `dal.DB` methods through | Plan-time: audit `dalgo2sql.Database`'s exported method set. If a needed method is unexported, file a follow-up against `dalgo2sql`. |
| Must-be-true | `mattn/go-sqlite3` exposes `ALTER TABLE DROP COLUMN` (requires SQLite 3.35.0+) and `RENAME COLUMN` (requires 3.25.0+) | Plan-time: verify the embedded SQLite version. `go-sqlite3 v1.14.x` ships with SQLite ≥ 3.40, so both are available. |
| Must-be-true | The `dbschema.SchemaReader`, `ddl.SchemaModifier`, `ddl.TransactionalDDL`, and `dal.ConcurrencyAware` interfaces in `dal-go/dalgo` are stable enough to implement against | Resolved: those interfaces shipped in the dbschema-modification + concurrency-capability Feature batches and are tagged. |
| Should-be-true | SQLite's `pragma_table_info` + `pragma_index_list` + `pragma_index_info` carry every shape `dbschema.CollectionDef` requires | Plan-time: prototype against Chinook; record any gap. |
| Should-be-true | The single-writer cap (`SupportsConcurrentConnections() = false`) is sufficient for consumers; the WAL-mode reader-concurrency distinction is not needed | Carried; documented in REQ:concurrency-aware-false. |

## Acceptance Criteria

### AC: new-database-opens-existing-file

**Requirements:** dbschema-ddl-coverage#req:new-database-constructor, dbschema-ddl-coverage#req:ping-on-open

**Given** a valid SQLite database file at `./testdata/chinook.db`
**When** the caller invokes `dalgo2sqlite.NewDatabase("./testdata/chinook.db")`
**Then** the call returns `(db, nil)`; `db.(dal.DB) != nil`; `db.(dbschema.SchemaReader) != nil`; `db.(ddl.SchemaModifier) != nil`; `db.(dal.ConcurrencyAware) != nil`; a follow-up `db.Close()` succeeds.

### AC: new-database-rejects-malformed-file

**Requirements:** dbschema-ddl-coverage#req:ping-on-open

**Given** a file at `./testdata/not-a-database.txt` whose contents are arbitrary ASCII text
**When** the caller invokes `dalgo2sqlite.NewDatabase("./testdata/not-a-database.txt")`
**Then** the call returns `(nil, err)` where `err` wraps the underlying `*sql.DB.PingContext` error; the file is NOT modified; no SQLite resources leak (the inner `*sql.DB` is closed before returning).

### AC: supports-concurrent-connections-false

**Requirements:** dbschema-ddl-coverage#req:concurrency-aware-false

**Given** any `dalgo2sqlite.Database` value
**When** the caller invokes `db.SupportsConcurrentConnections()`
**Then** the return value is `false`.

### AC: list-collections-excludes-internal-tables

**Requirements:** dbschema-ddl-coverage#req:list-collections

**Given** a SQLite database with three user tables (`users`, `orders`, `audit_log`) and an `sqlite_sequence` table created implicitly by an `INTEGER PRIMARY KEY AUTOINCREMENT` column
**When** the caller invokes `reader.ListCollections(ctx, nil)`
**Then** the result is a `[]dal.CollectionRef` of length 3 whose `Name` fields are `"audit_log"`, `"orders"`, `"users"` in alphabetical order; `sqlite_sequence` is absent; the second return is `nil`.

### AC: describe-collection-roundtrips-raw-sql-fields

**Requirements:** dbschema-ddl-coverage#req:describe-collection, dbschema-ddl-coverage#req:type-mapping

**Given** a SQLite table created via raw SQL `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, signup_at TEXT, balance NUMERIC DEFAULT 0)` (note: raw SQL, NOT via `CreateCollection` — so no driver-internal Time marker is written)
**When** the caller invokes `reader.DescribeCollection(ctx, &dal.CollectionRef{Name:"users"})`
**Then** the result is a `*dbschema.CollectionDef` with `Name=="users"`, `PrimaryKey==["id"]`, and `Fields` in order: `{Name:"id", Type:Int, AutoIncrement:false, Nullable:false}`, `{Name:"email", Type:String, Nullable:false}`, `{Name:"signup_at", Type:String, Nullable:true}` (falls back to `String` because no Time marker is present for this raw-SQL table), `{Name:"balance", Type:Decimal, Nullable:true, Default:{...numeric 0...}}`. The Time round-trip via `CreateCollection`-written marker is exercised by AC:create-then-describe-roundtrip instead.

### AC: describe-collection-not-found

**Requirements:** dbschema-ddl-coverage#req:describe-collection

**Given** a SQLite database with no tables (or a table list that does not include `nonexistent`)
**When** the caller invokes `reader.DescribeCollection(ctx, &dal.CollectionRef{Name:"nonexistent"})`
**Then** the result is `(nil, err)` where `err.Error()` contains the substring `"not found"` AND the substring `"nonexistent"`. The exact error type is plan-time-determined (see Outstanding Questions); the AC pins only the message-content contract.

### AC: create-then-describe-roundtrip

**Requirements:** dbschema-ddl-coverage#req:create-collection, dbschema-ddl-coverage#req:create-describe-round-trip, dbschema-ddl-coverage#req:type-mapping

**Given** a fresh empty SQLite database and a `dbschema.CollectionDef` `c` for a `users` table with primary key `id` (`Int`, AutoIncrement), columns `email` (`String`), `signup_at` (`Time`), `balance` (`Decimal`), and one secondary index on `email`
**When** the caller invokes `ddl.CreateCollection(ctx, db, c)` then `reader.DescribeCollection(ctx, &dal.CollectionRef{Name:"users"})`
**Then** the second call returns a `*dbschema.CollectionDef` whose `Name`, `Fields` (names + types + AutoIncrement + Nullable — including `signup_at` round-tripping as `Time` because `CreateCollection` wrote the per-column marker per REQ:type-mapping), `PrimaryKey`, and `Indexes` are semantically equal to `c`. The `Length` hint on `email`, if originally set, is dropped on round-trip (SQLite ignores VARCHAR length); the test accepts the lossy match.

### AC: create-collection-rejects-null-type

**Requirements:** dbschema-ddl-coverage#req:create-collection

**Given** a `dbschema.CollectionDef` whose `Fields` includes one entry with `Type == dbschema.Null`
**When** the caller invokes `ddl.CreateCollection(ctx, db, c)`
**Then** the call returns a non-nil error before any SQL execution; the error message names the offending field. The database state is unchanged (no partial `CREATE TABLE`).

### AC: create-collection-if-not-exists

**Requirements:** dbschema-ddl-coverage#req:create-collection

**Given** a SQLite database that already has a `users` table and a `CollectionDef` `c` describing `users`
**When** the caller invokes `ddl.CreateCollection(ctx, db, c, ddl.WithIfNotExists())`
**Then** the call returns `nil`; no error is raised about the pre-existing table; the existing `users` table's contents are unchanged.

### AC: drop-collection

**Requirements:** dbschema-ddl-coverage#req:drop-collection

**Given** a SQLite database with a `users` table (and its auto-created PK index)
**When** the caller invokes `ddl.DropCollection(ctx, db, "users")`
**Then** the call returns `nil`; a subsequent `reader.ListCollections(ctx)` does not include `"users"`; querying the `sqlite_master` table for indexes returns no rows belonging to `users`.

### AC: drop-collection-if-exists

**Requirements:** dbschema-ddl-coverage#req:drop-collection

**Given** a SQLite database with NO `users` table
**When** the caller invokes `ddl.DropCollection(ctx, db, "users", ddl.WithIfExists())`
**Then** the call returns `nil`; the database state is unchanged.

### AC: alter-add-drop-rename-field

**Requirements:** dbschema-ddl-coverage#req:alter-collection

**Given** a SQLite database with a `users(id, email)` table containing 3 rows
**When** the caller invokes `ddl.AlterCollection(ctx, db, "users", ddl.AddField(FieldDef{Name:"age", Type:Int, Nullable:true}), ddl.RenameField("email", "email_address"), ddl.DropField("age"))`
**Then** the call returns `nil` after applying ops in order; a follow-up `DescribeCollection` shows columns `id`, `email_address` (no `email`, no `age`); the original 3 rows survive with `id` and `email_address` (renamed) intact.

### AC: alter-modify-field-preserves-data

**Requirements:** dbschema-ddl-coverage#req:alter-collection, dbschema-ddl-coverage#req:transactional-ddl

**Given** a SQLite database with a `users(id INTEGER PRIMARY KEY, email TEXT)` table containing 3 rows
**When** the caller invokes `ddl.AlterCollection(ctx, db, "users", ddl.ModifyField("email", FieldDef{Name:"email", Type:String, Nullable:false}))` (changing nullability from true to false)
**Then** the call returns `nil`; the `users` table now has the modified `email NOT NULL` constraint; all 3 rows from the original table are present (data preservation through the migration dance); the operation executed atomically (if any sub-step had failed, no partial state would remain).

### AC: alter-add-drop-index

**Requirements:** dbschema-ddl-coverage#req:alter-collection

**Given** a SQLite database with a `users(id, email)` table and no secondary indexes
**When** the caller invokes `ddl.AlterCollection(ctx, db, "users", ddl.AddIndex(IndexDef{Name:"ix_users_email", Fields:[]string{"email"}}), ddl.DropIndex("ix_users_email"))`
**Then** the call returns `nil`; after the AddIndex sub-step the index existed (verifiable by `pragma_index_list("users")` mid-flight in a separate connection — but per REQ:transactional-ddl the whole AlterCollection runs in one transaction so external observers don't see the intermediate state); the final state is no `ix_users_email` index.

### AC: transactional-rollback-on-failure

**Requirements:** dbschema-ddl-coverage#req:transactional-ddl

**Given** a SQLite database with a `users` table and a multi-step `AlterCollection` call whose third sub-step is invalid SQL (e.g. dropping a non-existent column without `IfExists` semantics)
**When** the caller invokes `ddl.AlterCollection(ctx, db, "users", op1_valid, op2_valid, op3_invalid)`
**Then** the call returns a non-nil error naming the failing op; the `users` table is in the EXACT same state as before the call (the first two valid ops are rolled back).

### AC: not-supported-error-for-unknown-collection-type

**Requirements:** dbschema-ddl-coverage#req:describe-collection

**Given** a SQLite database with a table `weird_table(id INTEGER PRIMARY KEY, body MY_CUSTOM_TYPE)` (a column declared with an engine-foreign type)
**When** the caller invokes `reader.DescribeCollection(ctx, &dal.CollectionRef{Name:"weird_table"})`
**Then** the call returns `(nil, *dbschema.NotSupportedError)`; the error's `Op` field is `"DescribeCollection"`; the `Reason` field names the column `body` and the unrecognized type `MY_CUSTOM_TYPE`.

## Outstanding Questions

- **Time storage marker.** REQ:type-mapping pins that `Time` round-trips via the driver's writes-during-CreateCollection mechanism, but the mechanism itself (per-column comment via `COMMENT ON COLUMN` extension — SQLite doesn't have it natively; or a sidecar `_dalgo_meta` table) is plan-time. Pick at plan time.
- **Collection-not-found error type.** The `dbschema` package currently exports only `*NotSupportedError` (no `NotFoundError`). REQ:describe-collection's contract is therefore content-based (`err.Error()` contains `"not found"` + collection name). Plan-time options: (a) add `dbschema.NotFoundError` to `dal-go/dalgo` as a sibling Feature first, then this Feature consumes it; (b) define `dalgo2sqlite.ErrCollectionNotFound` locally and document the contract. Decide at plan time. Either way the AC passes by the message-content rule.
- **`SchemaReader` 5-method requirement.** The `SchemaReader` interface declares five methods; `ListConstraints` and `ListReferrers` are formally optional (drivers may return `*NotSupportedError`), but for SQLite they're cheaply implementable via `PRAGMA foreign_key_list`. The spec implements all five for full satisfaction. Plan-time can decide whether to stub `ListConstraints`/`ListReferrers` with `*NotSupportedError` if their pragma-driven implementation proves more complex than expected.
- **Pinning the dalgo version.** `dalgo2sqlite`'s `go.mod` MUST pin a `dalgo` version that has `dbschema.SchemaReader`, `ddl.SchemaModifier`, `ddl.TransactionalDDL`, and `dal.ConcurrencyAware`. As of this Feature spec, those interfaces shipped on `dalgo` `main` but are not yet tagged. Plan-time: either consume a SHA pin OR wait for the next dalgo tag.
- **`dalgo2sql.Database` exported surface.** The composition pattern (REQ:dalgo2sql-delegation) requires every `dal.DB` method to be reachable from outside the dalgo2sql package. Plan-time: audit; if any method is unexported, a small `dalgo2sql` follow-up Feature exports it (this Feature does NOT modify dalgo2sql but flags the gap if found).

---
*This document follows the https://specscore.md/feature-specification*
