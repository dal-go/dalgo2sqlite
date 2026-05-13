package dalgo2sqlite

// SupportsTransactionalDDL reports that SQLite supports transactional
// DDL — every CREATE / DROP / ALTER statement can be wrapped in a
// BEGIN/COMMIT and is rolled back atomically on commit failure.
func (d *Database) SupportsTransactionalDDL() bool { return true }
