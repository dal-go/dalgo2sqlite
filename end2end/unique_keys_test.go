package end2end

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dalgotest"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	dalrecord "github.com/dal-go/record"
	_ "modernc.org/sqlite"
)

// The rule every read keeps: within one read, no two records have the same key (a record
// with no key, which dalgotest.HasNoKey says, may repeat). The fixtures are ones a constant
// key cannot pass by accident: hundreds of rows with repeated non-key values, a composite
// key, no key at all and rows that are all alike.
const (
	invoiceRows    = 412
	uniqueKeysSeed = `
CREATE TABLE Invoice (InvoiceId INTEGER NOT NULL PRIMARY KEY, CustomerId INTEGER NOT NULL, BillingCountry TEXT, Total NUMERIC);
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 412)
INSERT INTO Invoice SELECT i, 1 + (i % 59), CASE i % 4 WHEN 0 THEN 'USA' WHEN 1 THEN 'France' ELSE 'Brazil' END, 0.99 + (i % 3) FROM n;
CREATE TABLE Customer (CustomerId INTEGER NOT NULL PRIMARY KEY, Country TEXT);
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 59)
INSERT INTO Customer SELECT i, CASE i % 2 WHEN 0 THEN 'USA' ELSE 'France' END FROM n;
CREATE TABLE CompositePk (Region TEXT NOT NULL, Code INTEGER NOT NULL, Label TEXT, PRIMARY KEY (Region, Code));
INSERT INTO CompositePk VALUES ('eu', 1, 'same'), ('eu', 2, 'same'), ('us', 1, 'same'), ('us', 2, 'same');
CREATE TABLE NoPk (Name TEXT, Qty INTEGER);
INSERT INTO NoPk VALUES ('bolt', 10), ('bolt', 10), ('nut', 10), ('bolt', 20), ('bolt', 10);
CREATE VIEW InvoiceView AS SELECT InvoiceId, BillingCountry FROM Invoice;
`
)

func col(e dal.Expression) dal.Column { return dal.Column{Expression: e} }

func uniqueKeysReads() []struct {
	name  string
	query dal.Query
	rows  int
} {
	from := func(name, alias string) dal.FromSource { return dal.From(dal.NewRootCollectionRef(name, alias)) }
	join := func(left, right, la, ra string) dal.FromSource {
		return from(left, la).Join(dal.NewJoinedSource(dal.NewRootCollectionRef(right, ra), dal.JoinInner,
			dal.NewComparison(dal.NewFieldRef(la, "CustomerId"), dal.Equal, dal.NewFieldRef(ra, "CustomerId"))))
	}
	return []struct {
		name  string
		query dal.Query
		rows  int
	}{
		{"single-PK select-all", from("Invoice", "").NewQuery().SelectColumns(), invoiceRows},
		{"single-PK select columns without key", from("Invoice", "").NewQuery().SelectColumns(
			col(dal.NewFieldRef("", "BillingCountry")), col(dal.NewFieldRef("", "Total"))), invoiceRows},
		{"single-PK select columns with key", from("Invoice", "").NewQuery().SelectColumns(
			col(dal.NewFieldRef("", "InvoiceId")), col(dal.NewFieldRef("", "BillingCountry"))), invoiceRows},
		{"composite-PK select-all", from("CompositePk", "").NewQuery().SelectColumns(), 4},
		{"composite-PK select columns", from("CompositePk", "").NewQuery().SelectColumns(col(dal.NewFieldRef("", "Label"))), 4},
		{"no-PK select-all", from("NoPk", "").NewQuery().SelectColumns(), 5},
		{"no-PK select columns", from("NoPk", "").NewQuery().SelectColumns(col(dal.NewFieldRef("", "Name"))), 5},
		{"view", from("InvoiceView", "").NewQuery().SelectColumns(), invoiceRows},
		{"group by", from("Invoice", "").NewQuery().GroupBy(dal.NewFieldRef("", "BillingCountry")).
			SelectColumns(col(dal.NewFieldRef("", "BillingCountry")), dal.Count()), 3},
		{"join one-to-many", join("Customer", "Invoice", "c", "i").NewQuery().SelectColumns(
			col(dal.NewFieldRef("c", "Country")), col(dal.NewFieldRef("i", "Total"))), invoiceRows},
		{"join many-to-one", join("Invoice", "Customer", "i", "c").NewQuery().SelectColumns(
			col(dal.NewFieldRef("i", "Total")), col(dal.NewFieldRef("c", "Country"))), invoiceRows},
	}
}

func drain(t *testing.T, reader dal.RecordsReader) []dalrecord.Record {
	t.Helper()
	defer func() { _ = reader.Close() }()
	var records []dalrecord.Record
	for {
		r, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) {
			return records
		}
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
}

func TestEveryReadKeepsItsKeysUniqueWithinTheRead(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "unique.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, uniqueKeysSeed); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []struct {
		name string
		opts dalgo2sql.DbOptions
	}{
		{"default options", dalgo2sql.DbOptions{}},
		{"sqlite dialect", dalgo2sql.DbOptions{StructuredQueryDialect: "sqlite"}},
	} {
		db, err := dalgo2sqlite.NewDatabaseWithOptions(path, dal.NewSchema(nil, nil), opts.opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		for _, read := range uniqueKeysReads() {
			check := func(t *testing.T, records []dalrecord.Record) {
				t.Helper()
				if len(records) != read.rows {
					t.Fatalf("read %d records, want %d", len(records), read.rows)
				}
				dalgotest.AssertUniqueKeys(t, records)
				for i, r := range records {
					if dalgotest.HasNoKey(r.Key()) {
						t.Fatalf("record %d has no key", i)
					}
					if r.Key().ID == "__dalgo_record_id" {
						t.Fatalf("record %d has the old constant key", i)
					}
				}
			}
			if strings.HasPrefix(read.name, "join") && opts.opts.StructuredQueryDialect != "sqlite" {
				continue // joins need the SQLite structured-query dialect; the default options refuse them
			}
			t.Run(opts.name+"/"+read.name, func(t *testing.T) {
				if read.name == "join one-to-many" {
					// dalgo's federated join (dal/join_execute.go, joinExecution.build) gives every
					// joined row its first source's key, so a one-to-many join repeats it. The
					// read inside a transaction runs natively and is checked below.
					t.Skip("upstream dalgo violation: one-to-many join repeats the root record key")
				}
				reader, err := db.ExecuteQueryToRecordsReader(ctx, read.query)
				if err != nil {
					t.Fatal(err)
				}
				check(t, drain(t, reader))
			})
			t.Run(opts.name+"/"+read.name+" in a transaction", func(t *testing.T) {
				err := db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
					reader, err := tx.ExecuteQueryToRecordsReader(ctx, read.query)
					if err != nil {
						return err
					}
					check(t, drain(t, reader))
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
