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

## Open question: migrate to a pure-Go SQLite driver?

This package currently depends on `github.com/mattn/go-sqlite3`, a cgo binding
to the C SQLite library. That forces `CGO_ENABLED=1`, which:

- requires a C toolchain in CI (we now pass `cgo_enabled: true` to the shared
  `strongo/go-ci-action` workflow, against its `CGO_ENABLED=0` default), and
- prevents single static binaries and easy cross-compilation for downstream
  consumers (e.g. `datatug-cli`).

A pure-Go alternative — **`modernc.org/sqlite`** — exposes the same
`database/sql` driver interface (so `dalgo2sql` keeps working) and builds with
`CGO_ENABLED=0`, restoring static/cross builds and dropping the C toolchain
requirement.

Open points to decide before migrating:

- **Behavior/parity:** confirm the SQLite features we rely on (pragmas,
  `sqlite_master` introspection, single-writer concurrency) behave identically.
- **Performance:** `modernc.org/sqlite` is a transpilation of SQLite to Go;
  benchmark hot paths vs the cgo driver for our workloads.
- **Footprint:** it's a large module — weigh build size/time against the cgo
  toolchain cost it removes.
- **Driver name:** registers as `sqlite` (vs `sqlite3` for mattn) — audit any
  hard-coded driver-name strings in consumers.

If we migrate, the `cgo_enabled: true` flags added to this repo's and
`datatug-cli`'s CI workflows can be removed.
