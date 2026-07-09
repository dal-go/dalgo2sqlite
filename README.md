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
