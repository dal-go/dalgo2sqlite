# dalgo2sqlite

SQLite-specific DALgo driver. Wraps `github.com/dal-go/dalgo2sql` to provide the `dal.DB` surface, and adds SQLite-native implementations of:

- `dbschema.Adapter` — schema introspection via `sqlite_master` and `pragma_table_info`
- `ddl.Applier` — SQLite-flavored `CREATE TABLE` / `CREATE INDEX` / `DROP TABLE` etc.
- `dal.ConcurrencyAware` — advertises `Concurrency() = 1` for write paths (SQLite is single-writer)

Used by consumers (e.g. `datatug-cli`'s `db copy`) that need schema-modification and concurrency hints through the unified DALgo abstraction without hand-rolling engine-specific SQL.

<!-- dev-approach:v1 -->
## Our approach to development

We build with our own tooling:

- **[SpecScore](https://specscore.md)** — specify requirements as `SpecScore.md` artifacts
- **[SpecStudio](https://specscore.studio)** — author & manage specs across their lifecycle
- **[inGitDB](https://ingitdb.com)** — store structured data in Git where applicable
- **[DALgo](https://dalgo.io)** — data access layer for Go
- **[cover100.dev](https://cover100.dev)** — drive toward 100% test coverage
- **[DataTug](https://datatug.io)** — query & explore data
<!-- /dev-approach -->

## SQLite driver: `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0`)

This package uses **`modernc.org/sqlite`** — a pure-Go transpilation of the
SQLite C library — instead of the former `github.com/mattn/go-sqlite3` cgo
binding.  The migration was done because:

- The cgo driver required a C toolchain in every build environment (CI,
  containers, cross-compilation targets).
- It prevented single static binaries for downstream consumers such as
  `datatug-cli`.
- `modernc.org/sqlite` exposes the same `database/sql` interface (driver name
  `"sqlite"` instead of `"sqlite3"`), so `dalgo2sql` continues to work
  unchanged.
- All existing SQLite features relied on here — `sqlite_master` introspection,
  `PRAGMA` table/index queries, transactional DDL — behave identically under
  the pure-Go driver.

The `cgo_enabled: true` flag can be removed from this repo's CI workflow and
from any downstream workflow that no longer needs cgo for other reasons.

## TugQL JOIN acceptance

The checked-in `testdata/joins` directory contains a small Chinook-shaped
SQLite seed, the canonical nested Invoice/Customer/Employee TugQL query, and
expected rows. The test loads the seed into a temporary database, runs the
query through both the database and read-transaction entry points, and checks
ordered results and LEFT JOIN null extension. A second query carries independent
algorithm preferences on outer, nested, and sibling JOINs; SQLite's native
execution may ignore those physical preferences and must return the same rows.
The tests also check the canonical fixture hashes, their DALgo source commit,
and the expected diagnostics for malformed JOIN references.

Run the complete acceptance journey from this repository's root:

```sh
go test ./end2end -run 'TestTugQL(NestedJoinChinook|JoinFixtureManifest|JoinNegativeFixtures)' -count=1
```

## Optional pinned table reads

`OpenTableRead` returns an owned `TableReadSpan` for one existing absolute
SQLite file. Its first supported profile requires the exact native file route,
UTF-8 encoding, explicit `main`, and a simple ASCII ordinary-table name.
Views, virtual/shadow/generated tables, SQLite internal tables, attachments,
temp databases and implicit or rewritten identifiers are refused. Existing
`NewDatabase` behavior is unchanged.

The span privately retains one `mode=ro` connection and a rollback-only
transaction. It establishes `query_only`, native limits, exact catalog spelling
and ordinary columns before a data reader is available. `Facts` subsequently
returns cloned immutable metadata and checks liveness without issuing SQL.
`Session` exposes only DALgo read methods with a fixed SQLite compiler and
native values; every call remains linked to the original span cancellation and
the earlier caller/span deadline. Readers retain that context until close,
EOF or error. Always close readers and the span and inspect their errors.
Observed cleanup errors are returned; `database/sql` may consume an error from
its automatic rollback after cancellation, so universal rollback diagnostics
are not promised.

`MaxBytes` limits combined accepted native value bytes, with a separate
`MaxColumns` ceiling. After discovering the actual column count `N`, the span
sets and reads back `SQLITE_LIMIT_LENGTH` as `floor(MaxBytes/N)`, requiring at
least eight bytes per column. The actual bound is reported as `MaxCellBytes`.
UTF-8 admission avoids native UTF-16-to-UTF-8 expansion. These are conservative
all-or-nothing checks: a cell below `MaxBytes` can still exceed `MaxCellBytes`
and refuse the operation. There is no filtering or truncation. Row count,
serialized output, supported value classes, finite numbers and valid UTF-8
still need independent host bounds/validation. SQLite row headers, pager/cache,
opaque driver storage and Go heap overhead are outside this byte bound.

These facts are neither permissions nor authenticated source identity. The
host must authorize before opening, secure the returned DALgo session, and
independently bind its owned/fixed private file route to the authenticated
source. `Filename` describes SQLite's logical opened-file route; it does not
attest OS inode identity or protect against external filesystem replacement.
Caller-owned records and opaque driver buffers are not covered by a purge
promise. The owned temporary-file tests establish this optional API profile;
they do not certify every SQLite configuration or activate any application
security routes.
