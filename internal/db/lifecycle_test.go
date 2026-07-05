package db

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLifecycle exercises the full flow: create image, mount, initdb, start,
// query over the Unix socket, stop, unmount, then remount and verify the
// data survived.
func TestLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	for _, tool := range []string{"fuse2fs"} {
		if _, err := findTool(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	if _, err := findTool("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available")
	}
	if _, err := FindBinDir(); err != nil {
		t.Skip("PostgreSQL server binaries not available")
	}

	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", filepath.Join(base, "state"))
	image := filepath.Join(base, "test.pdb")

	d, err := New(image)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Down() })

	info, started, err := d.Up(UpOptions{Size: 300 << 20})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !started {
		t.Error("Up should report that it started the server")
	}

	psql := func(sql string) string {
		out, err := exec.Command("psql", info.URL(), "-Atc", sql).CombinedOutput()
		if err != nil {
			t.Fatalf("psql %q: %v: %s", sql, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	psql("create table t (n int)")
	psql("insert into t values (42)")

	if got := psql("show synchronous_commit"); got != "off" {
		t.Errorf("synchronous_commit = %q by default, want off", got)
	}

	// A second Up should find the server already running.
	if _, started, err := d.Up(UpOptions{}); err != nil || started {
		t.Errorf("second Up: started=%v err=%v, want already-running", started, err)
	}

	if err := d.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if mounted, err := d.Mounted(); err != nil || mounted {
		t.Errorf("after Down: mounted=%v err=%v", mounted, err)
	}
	if info, err := d.Running(); err != nil || info != nil {
		t.Errorf("after Down: running=%v err=%v", info, err)
	}

	// Data must survive a stop/start cycle.
	info, started, err = d.Up(UpOptions{Durable: true})
	if err != nil {
		t.Fatalf("second Up after Down: %v", err)
	}
	if !started {
		t.Error("Up after Down should start the server")
	}
	if got := psql("select n from t"); got != "42" {
		t.Errorf("data did not survive remount: got %q, want 42", got)
	}
	if got := psql("show synchronous_commit"); got != "on" {
		t.Errorf("synchronous_commit = %q with Durable, want on", got)
	}
	if err := d.Down(); err != nil {
		t.Fatalf("final Down: %v", err)
	}
}
