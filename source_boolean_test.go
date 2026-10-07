package dalgo2sqlite

import (
	"context"
	"io"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

func TestBooleanSourceRowsUseStrictBooleanValues(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TABLE flags (flag BOOLEAN, ordinary INTEGER)`,
		`INSERT INTO flags VALUES (TRUE, 41), (FALSE, 42), (NULL, 43)`,
	} {
		if _, err := db.sqlDB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	ref := dal.NewRootCollectionRef("flags", "")
	definition, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Fields) != 2 || definition.Fields[0].Type != dbschema.Bool || definition.Fields[1].Type != dbschema.Int {
		t.Fatalf("BOOLEAN and INTEGER schema types: %+v", definition.Fields)
	}
	if got := definition.SourceDefinition.Columns[0].DeclaredType; got != "BOOLEAN" {
		t.Fatalf("BOOLEAN declaration metadata = %q", got)
	}
	cursor, err := db.OpenSourceRows(ctx, &ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cursor.Close() }()
	for _, want := range []struct {
		flag    any
		integer int64
		class   string
	}{
		{true, 41, "integer"},
		{false, 42, "integer"},
		{nil, 43, "null"},
	} {
		row, nextErr := cursor.Next()
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if row.Values["flag"] != want.flag || row.StorageClasses["flag"] != want.class || row.Values["ordinary"] != want.integer || row.StorageClasses["ordinary"] != "integer" {
			t.Fatalf("source row = %+v, want flag=%#v integer=%d class=%s", row, want.flag, want.integer, want.class)
		}
	}
	if _, err := cursor.Next(); err != io.EOF {
		t.Fatalf("end of boolean source rows: %v", err)
	}
}

func TestBooleanSourceRowsRejectInvalidPhysicalValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "integer outside boolean domain", value: "2"},
		{name: "real", value: "1.5"},
		{name: "text", value: "'true'"},
		{name: "blob", value: "X'01'"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			ctx := context.Background()
			name := "bad_boolean_" + string(rune('a'+i))
			for _, statement := range []string{
				"CREATE TABLE " + name + " (flag BOOLEAN)",
				"INSERT INTO " + name + " VALUES (" + tt.value + ")",
			} {
				if _, err := db.sqlDB.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			ref := dal.NewRootCollectionRef(name, "")
			cursor, err := db.OpenSourceRows(ctx, &ref)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cursor.Close() }()
			if _, err := cursor.Next(); err == nil {
				t.Fatal("invalid physical value was accepted as BOOLEAN")
			}
		})
	}
}
