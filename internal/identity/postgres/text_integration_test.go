package postgres

import (
	"context"
	"testing"

	"github.com/1yoouoo/mcpaste/internal/testdb"
)

func TestTextMigrationTablesPresent(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	for _, table := range []string{"pastes", "paste_revisions"} {
		var present bool
		if err := pool.QueryRow(ctx, `
select to_regclass(current_schema() || '.' || $1) is not null`, table).Scan(&present); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if !present {
			t.Fatalf("table %s is absent", table)
		}
	}
}

func TestTextRevisionRetentionAndTombstoneContract(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	var constraintCount int
	if err := pool.QueryRow(ctx, `
select count(*)
from pg_constraint
where conrelid = 'paste_revisions'::regclass
  and conname = 'paste_revisions_exact_retention'`).Scan(&constraintCount); err != nil {
		t.Fatalf("inspect retention constraint: %v", err)
	}
	if constraintCount != 1 {
		t.Fatalf("retention constraint count = %d, want 1", constraintCount)
	}
}
