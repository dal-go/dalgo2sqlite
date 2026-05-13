package end2end

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo2sqlite"
)

// TestE2E_CreateDescribeRoundTrip exercises the Feature's round-trip
// contract: CreateCollection writes the table; DescribeCollection
// reads the same shape back. AlterCollection coverage waits on the
// upstream dalgo ddl.Applier Feature.
func TestE2E_CreateDescribeRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := dalgo2sqlite.NewDatabase(filepath.Join(dir, "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	c := dbschema.CollectionDef{
		Name: "tracks",
		Fields: []dbschema.FieldDef{
			{Name: dal.FieldName("track_id"), Type: dbschema.Int, AutoIncrement: true},
			{Name: dal.FieldName("name"), Type: dbschema.String, Nullable: false},
			{Name: dal.FieldName("milliseconds"), Type: dbschema.Int, Nullable: true},
			{Name: dal.FieldName("price"), Type: dbschema.Decimal, Nullable: false},
			{Name: dal.FieldName("created_at"), Type: dbschema.Time, Nullable: true},
		},
		PrimaryKey: []dal.FieldName{"track_id"},
		Indexes: []dbschema.IndexDef{
			{Name: "ix_tracks_name", Collection: "tracks", Fields: []dal.FieldName{"name"}},
		},
	}
	if err := ddl.CreateCollection(ctx, db, c); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	ref := dal.NewRootCollectionRef("tracks", "")
	got, err := db.DescribeCollection(ctx, &ref)
	if err != nil {
		t.Fatalf("DescribeCollection: %v", err)
	}
	if got.Name != "tracks" {
		t.Errorf("Name = %q, want tracks", got.Name)
	}
	if len(got.Fields) != 5 {
		t.Fatalf("Fields len = %d, want 5", len(got.Fields))
	}

	wantFields := []dbschema.FieldDef{
		{Name: "track_id", Type: dbschema.Int, AutoIncrement: true, Nullable: false},
		{Name: "name", Type: dbschema.String, Nullable: false},
		{Name: "milliseconds", Type: dbschema.Int, Nullable: true},
		{Name: "price", Type: dbschema.Decimal, Nullable: false},
		{Name: "created_at", Type: dbschema.Time, Nullable: true},
	}
	for i, want := range wantFields {
		g := got.Fields[i]
		if string(g.Name) != string(want.Name) {
			t.Errorf("Fields[%d].Name = %q, want %q", i, g.Name, want.Name)
		}
		if g.Type != want.Type {
			t.Errorf("Fields[%d].Type = %v, want %v (field %s)", i, g.Type, want.Type, g.Name)
		}
		if g.AutoIncrement != want.AutoIncrement {
			t.Errorf("Fields[%d].AutoIncrement = %v, want %v (field %s)", i, g.AutoIncrement, want.AutoIncrement, g.Name)
		}
		if g.Nullable != want.Nullable {
			t.Errorf("Fields[%d].Nullable = %v, want %v (field %s)", i, g.Nullable, want.Nullable, g.Name)
		}
	}
	if len(got.PrimaryKey) != 1 || string(got.PrimaryKey[0]) != "track_id" {
		t.Errorf("PrimaryKey = %v, want [track_id]", got.PrimaryKey)
	}
	if len(got.Indexes) != 1 || got.Indexes[0].Name != "ix_tracks_name" {
		t.Errorf("Indexes = %+v, want one entry named ix_tracks_name", got.Indexes)
	}
}
