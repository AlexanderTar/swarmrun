package db_test

import (
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/db"
)

func TestLowerFuncFoldsUnicodeAndPassesOtherTypesThrough(t *testing.T) {
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var text, blob string
	if err := d.QueryRow("SELECT "+db.LowerFunc+"('ÜBER Straße'), "+db.LowerFunc+"(CAST('ÄÖ' AS BLOB))").Scan(&text, &blob); err != nil {
		t.Fatal(err)
	}
	if text != "über straße" || blob != "äö" {
		t.Fatalf("got %q, %q", text, blob)
	}

	var null *string
	var num int64
	if err := d.QueryRow("SELECT "+db.LowerFunc+"(NULL), "+db.LowerFunc+"(42)").Scan(&null, &num); err != nil {
		t.Fatal(err)
	}
	if null != nil || num != 42 {
		t.Fatalf("got %v, %d", null, num)
	}
}
