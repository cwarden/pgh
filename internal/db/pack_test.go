package db

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugfsErrors(t *testing.T) {
	benign := "debugfs 1.46.5 (30-Dec-2021)\nrdump: Operation not permitted while changing ownership of /x/lost+found\n"
	if msg := debugfsErrors(benign); msg != "" {
		t.Errorf("benign output flagged as error: %q", msg)
	}
	missing := "debugfs 1.46.5 (30-Dec-2021)\nrdump: No such file or directory while statting out2\n"
	if msg := debugfsErrors(missing); msg == "" {
		t.Error("missing-destination error not detected")
	}
	badImage := "debugfs 1.46.5 (30-Dec-2021)\ndebugfs: No such file or directory while trying to open /nope\nrdump: Filesystem not open\n"
	if msg := debugfsErrors(badImage); msg == "" {
		t.Error("unopenable-image error not detected")
	}
}

// TestPackLifecycle exercises the pack backend end to end: initdb into a
// native directory, pack to create the image, reopen by unpacking, and
// verify data survives the round trip.
func TestPackLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if _, err := findMkfs(); err != nil {
		t.Skip("mke2fs not available")
	}
	if _, err := findTool("debugfs"); err != nil {
		t.Skip("debugfs not available")
	}
	if _, err := FindBinDir(); err != nil {
		t.Skip("PostgreSQL server binaries not available")
	}

	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", filepath.Join(base, "state"))
	t.Setenv("PGH_MOUNT", "pack")
	image := filepath.Join(base, "packed.pdb")

	d, err := New(image)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Down() })

	info, started, err := d.Up(UpOptions{Size: 128 << 20})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !started {
		t.Error("Up should report that it started the server")
	}
	if backend := d.MountBackend(); backend != "unpacked directory" {
		t.Errorf("MountBackend() = %q, want unpacked directory", backend)
	}
	// The image must exist as soon as the database does.
	if !d.ImageExists() {
		t.Error("image not created after Up on a new database")
	}

	psql := func(sql string) string {
		out, err := exec.Command("psql", info.URL(), "-Atc", sql).CombinedOutput()
		if err != nil {
			t.Fatalf("psql %q: %v: %s", sql, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	psql("create table t (n int)")
	psql("insert into t values (7)")

	if err := d.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if _, err := os.Stat(d.MountDir()); !os.IsNotExist(err) {
		t.Errorf("unpacked directory %s still exists after Down", d.MountDir())
	}

	// Reopen: the packed image must contain the committed data.
	info, _, err = d.Up(UpOptions{})
	if err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if got := psql("select n from t"); got != "7" {
		t.Errorf("data did not survive pack/unpack: got %q, want 7", got)
	}
	if err := d.Down(); err != nil {
		t.Fatalf("final Down: %v", err)
	}
}
