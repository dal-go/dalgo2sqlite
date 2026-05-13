# dalgo2sqlite

SQLite-specific DALgo driver. Wraps `github.com/dal-go/dalgo2sql` to provide the `dal.DB` surface, and adds SQLite-native implementations of:

- `dbschema.Adapter` — schema introspection via `sqlite_master` and `pragma_table_info`
- `ddl.Applier` — SQLite-flavored `CREATE TABLE` / `CREATE INDEX` / `DROP TABLE` etc.
- `dal.ConcurrencyAware` — advertises `Concurrency() = 1` for write paths (SQLite is single-writer)

Used by consumers (e.g. `datatug-cli`'s `db copy`) that need schema-modification and concurrency hints through the unified DALgo abstraction without hand-rolling engine-specific SQL.
