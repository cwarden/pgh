package monitor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/cwarden/pgh/internal/db"
)

// haveTool reports whether an external command is available, checking the
// sbin directories often absent from a non-root PATH.
func haveTool(name string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/local/sbin"} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

func TestSourceName(t *testing.T) {
	cases := map[string]string{
		"/tmp/temp.pdb":        "temp",
		"/var/db/big.image":    "big",
		"plain":                "plain",
		"/a/b/dotted.name.pdb": "dotted.name",
	}
	for in, want := range cases {
		if got := SourceName(in); got != want {
			t.Errorf("SourceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeQuery(t *testing.T) {
	if got := normalizeQuery("select\n  1,\n  2"); got != "select 1, 2" {
		t.Errorf("normalizeQuery = %q", got)
	}
	if got := normalizeQuery("   "); got != "" {
		t.Errorf("normalizeQuery blank = %q", got)
	}
}

// TestRefreshLive brings up a real database and checks that Refresh reports
// it, sees an active backend running a slow query, and computes a version.
func TestRefreshLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if !haveTool("fuse2fs") {
		t.Skip("fuse2fs not available")
	}
	if !haveTool("mkfs.ext4") {
		t.Skip("mkfs.ext4 not available")
	}
	if _, err := db.FindBinDir(); err != nil {
		t.Skip("PostgreSQL server binaries not available")
	}

	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", filepath.Join(base, "state"))
	image := filepath.Join(base, "mon.pdb")

	d, err := db.New(image)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Down() })

	info, _, err := d.Up(db.UpOptions{Size: 300 << 20})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	mon := New("")
	defer mon.Close()

	// First refresh: primes the counter baseline and connects.
	snap := mon.Refresh(context.Background())
	if len(snap.Servers) != 1 {
		t.Fatalf("Refresh saw %d servers, want 1", len(snap.Servers))
	}
	if s := snap.Servers[0]; s.Err != nil {
		t.Fatalf("server error: %v", s.Err)
	} else if s.Source != "mon" {
		t.Errorf("source = %q, want mon", s.Source)
	} else if s.Version == "" {
		t.Error("version is empty")
	}

	// Start a slow query in the background so a backend is active.
	psql := exec.Command("psql", info.URL(), "-Atc", "select pg_sleep(3)")
	if err := psql.Start(); err != nil {
		t.Fatalf("starting slow query: %v", err)
	}
	t.Cleanup(func() { psql.Process.Kill(); psql.Wait() })

	// Give the query a moment to register in pg_stat_activity.
	deadline := time.Now().Add(5 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		snap = mon.Refresh(context.Background())
		for _, b := range snap.Backends {
			if b.State == "active" && b.Source == "mon" {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !found {
		t.Error("no active backend seen for the running slow query")
	}
}
