package end2end

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	_ "modernc.org/sqlite"
)

// TestDTQLNestedJoinChinook is the package's reproducible, cross-module
// acceptance journey: load a small Chinook-shaped database, parse the public
// DTQL document, and execute the nested relation through the SQLite adapter.
func TestDTQLNestedJoinChinook(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "chinook-mini.db")
	seed, err := os.ReadFile(filepath.Join("..", "testdata", "joins", "chinook-mini.sql"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, string(seed)); err != nil {
		_ = raw.Close()
		t.Fatalf("seed Chinook: %v", err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	queryYAML, err := os.ReadFile(filepath.Join("..", "testdata", "joins", "chinook-nested.dtql.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const canonicalQueryDigest = "2eeef93ffb02a4272f06f6ab909b2db4cbbb9a2a1df280ae4c47851534f583b3"
	if digest := fmt.Sprintf("%x", sha256.Sum256(queryYAML)); digest != canonicalQueryDigest {
		t.Fatalf("DTQL fixture digest = %s, want %s", digest, canonicalQueryDigest)
	}
	query, err := dtql.Deserialize(queryYAML)
	if err != nil {
		t.Fatalf("parse DTQL: %v", err)
	}
	db, err := dalgo2sqlite.NewDatabaseWithOptions(dbPath, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{
		StructuredQueryDialect: "sqlite",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	reader, err := db.ExecuteQueryToRecordsReader(ctx, query)
	if err != nil {
		t.Fatalf("execute nested JOIN: %v", err)
	}
	got := readDTQLJoinRows(t, reader)
	wantBytes, err := os.ReadFile(filepath.Join("..", "testdata", "joins", "chinook-nested.rows.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want []map[string]any
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %#v, want %#v", got, want)
	}
	if err := db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		reader, err := tx.ExecuteQueryToRecordsReader(ctx, query)
		if err != nil {
			return err
		}
		transactionRows := readDTQLJoinRows(t, reader)
		if !reflect.DeepEqual(transactionRows, want) {
			t.Errorf("transaction rows = %#v, want %#v", transactionRows, want)
		}
		return nil
	}); err != nil {
		t.Fatalf("read transaction nested JOIN: %v", err)
	}

	// A native SQLite plan may ignore physical hints, while returning exactly
	// the same ordered logical result as the unhinted query.
	hintedYAML, err := os.ReadFile(filepath.Join("..", "testdata", "joins", "chinook-hinted.dtql.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	hintedQuery, err := dtql.Deserialize(hintedYAML)
	if err != nil {
		t.Fatalf("parse hinted DTQL: %v", err)
	}
	hintedReader, err := db.ExecuteQueryToRecordsReader(ctx, hintedQuery)
	if err != nil {
		t.Fatalf("execute hinted JOIN: %v", err)
	}
	if hintedRows := readDTQLJoinRows(t, hintedReader); !reflect.DeepEqual(hintedRows, want) {
		t.Errorf("hinted rows = %#v, want %#v", hintedRows, want)
	}
	if err := db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		reader, err := tx.ExecuteQueryToRecordsReader(ctx, hintedQuery)
		if err != nil {
			return err
		}
		if hintedRows := readDTQLJoinRows(t, reader); !reflect.DeepEqual(hintedRows, want) {
			t.Errorf("hinted transaction rows = %#v, want %#v", hintedRows, want)
		}
		return nil
	}); err != nil {
		t.Fatalf("read transaction hinted JOIN: %v", err)
	}

	wildcardYAML, err := os.ReadFile(filepath.Join("..", "testdata", "joins", "chinook-wildcard.dtql.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	wildcardQuery, err := dtql.Deserialize(wildcardYAML)
	if err != nil {
		t.Fatalf("parse wildcard DTQL: %v", err)
	}
	wantWildcard := []map[string]any{
		{"invoice_id": float64(3), "FirstName": "Bea", "employee": nil},
		{"invoice_id": float64(2), "FirstName": "Ada", "employee": "Evan"},
		{"invoice_id": float64(1), "FirstName": "Ada", "employee": "Evan"},
	}
	wildcardReader, err := db.ExecuteQueryToRecordsReader(ctx, wildcardQuery)
	if err != nil {
		t.Fatalf("execute wildcard JOIN: %v", err)
	}
	if wildcardRows := readDTQLJoinRows(t, wildcardReader); !reflect.DeepEqual(wildcardRows, wantWildcard) {
		t.Errorf("wildcard rows = %#v, want %#v", wildcardRows, wantWildcard)
	}
	if err := db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		reader, err := tx.ExecuteQueryToRecordsReader(ctx, wildcardQuery)
		if err != nil {
			return err
		}
		if wildcardRows := readDTQLJoinRows(t, reader); !reflect.DeepEqual(wildcardRows, wantWildcard) {
			t.Errorf("transaction wildcard rows = %#v, want %#v", wildcardRows, wantWildcard)
		}
		return nil
	}); err != nil {
		t.Fatalf("read transaction wildcard JOIN: %v", err)
	}
}

func TestDTQLJoinFixtureManifest(t *testing.T) {
	const directory = "../testdata/joins"
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SchemaVersion int               `json:"schemaVersion"`
		SourceCommit  string            `json:"sourceCommit"`
		Files         map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	const canonicalSourceCommit = "c999c7372fd944c57e89dad7e75f9f42c8afaae1"
	if manifest.SchemaVersion != 1 || manifest.SourceCommit != canonicalSourceCommit || len(manifest.Files) == 0 {
		t.Fatalf("invalid fixture manifest: %#v", manifest)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(manifest.Files)+2 { // manifest and SQLite seed
		t.Fatalf("fixture manifest has %d canonical files; directory has %d entries", len(manifest.Files), len(entries))
	}
	for name, want := range manifest.Files {
		fixture, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(fixture)); got != want {
			t.Errorf("fixture %s SHA-256 = %s, want %s", name, got, want)
		}
	}
}

func TestDTQLJoinNegativeFixtures(t *testing.T) {
	for _, name := range []string{"forward-alias", "unknown-alias"} {
		t.Run(name, func(t *testing.T) {
			query, err := os.ReadFile(filepath.Join("..", "testdata", "joins", name+".dtql.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(filepath.Join("..", "testdata", "joins", name+".error.json"))
			if err != nil {
				t.Fatal(err)
			}
			var want struct{ Category, Path string }
			if err := json.Unmarshal(expected, &want); err != nil {
				t.Fatal(err)
			}
			_, err = dtql.Deserialize(query)
			var diagnostic *dal.JoinValidationError
			if !errors.As(err, &diagnostic) {
				t.Fatalf("want JOIN diagnostic, got %v", err)
			}
			if diagnostic.Category != want.Category || diagnostic.Path != want.Path {
				t.Fatalf("diagnostic = %s at %s, want %s at %s", diagnostic.Category, diagnostic.Path, want.Category, want.Path)
			}
		})
	}
}

func readDTQLJoinRows(t *testing.T, reader dal.RecordsReader) []map[string]any {
	t.Helper()
	defer func() { _ = reader.Close() }()
	var rows []map[string]any
	for {
		rec, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			return rows
		}
		if readErr != nil {
			t.Fatalf("read nested JOIN: %v", readErr)
		}
		data, ok := rec.Data().(map[string]any)
		if !ok {
			t.Fatalf("row data is %T, want map[string]any", rec.Data())
		}
		if _, present := data["employee"]; !present {
			t.Fatalf("projected LEFT field is absent in row: %#v", data)
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		var normalized map[string]any
		if err := json.Unmarshal(encoded, &normalized); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, normalized)
	}
}
