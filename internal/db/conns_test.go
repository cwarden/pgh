package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestConnections starts a server and checks that client backends are counted,
// with the counting connection itself excluded.
func TestConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	for _, tool := range []string{"fuse2fs", "mkfs.ext4"} {
		if _, err := findTool(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	if _, err := FindBinDir(); err != nil {
		t.Skip("PostgreSQL server binaries not available")
	}

	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", filepath.Join(base, "state"))

	d, err := New(filepath.Join(base, "conns.pdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Down() })

	info, _, err := d.Up(UpOptions{Size: 300 << 20})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conns, err := info.Connections(ctx)
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if conns.Total != 0 {
		t.Errorf("Total = %d with no clients attached, want 0", conns.Total)
	}

	// An idle client counts toward the total but not toward non-idle.
	idle, err := pgx.Connect(ctx, info.URL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer idle.Close(ctx)
	if _, err := idle.Exec(ctx, "select 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}

	conns, err = info.Connections(ctx)
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if conns.Total != 1 {
		t.Errorf("Total = %d with one idle client, want 1", conns.Total)
	}
	if conns.Active != 0 {
		t.Errorf("Active = %d with one idle client, want 0", conns.Active)
	}
}
