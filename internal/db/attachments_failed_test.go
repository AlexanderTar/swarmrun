package db_test

import (
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/db"
)

func TestAgentOperationsAttachmentsFailedColumn(t *testing.T) {
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_operations') WHERE name = 'attachments_failed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("attachments_failed columns = %d, want 1", n)
	}
	var dflt string
	var notnull int
	if err := d.QueryRow(`SELECT dflt_value, "notnull" FROM pragma_table_info('agent_operations') WHERE name = 'attachments_failed'`).Scan(&dflt, &notnull); err != nil {
		t.Fatal(err)
	}
	if dflt != "0" || notnull != 1 {
		t.Fatalf("attachments_failed default = %q notnull = %d, want 0 and required", dflt, notnull)
	}
}
