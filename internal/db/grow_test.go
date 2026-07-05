package db

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestOnlineGrow exercises the autogrow path on a kernel-mounted database:
// the growth ceiling raises the file's apparent size at mount, nearly
// filling the filesystem triggers an online grow, and closing sheds the
// ceiling so the file shrinks back to the filesystem size.
func TestOnlineGrow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if _, err := findMkfs(); err != nil {
		t.Skip("mke2fs not available")
	}
	if _, err := FindBinDir(); err != nil {
		t.Skip("PostgreSQL server binaries not available")
	}

	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", filepath.Join(base, "state"))
	t.Setenv("PGH_GROW_HEADROOM", "1G")

	d, err := New(filepath.Join(base, "grow.pdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Down() })

	if _, _, err := d.Up(UpOptions{Size: 128 << 20}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if d.MountBackend() != "kernel mount" {
		t.Skip("udisks kernel mount not available; online grow does not apply")
	}

	// The mounted file should carry the growth ceiling.
	fi, err := os.Stat(d.Image)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() <= 128<<20 {
		t.Errorf("image size %d while mounted, want growth ceiling above 128M", fi.Size())
	}

	// A fresh 128M database has less than the 64M free-space cushion, so
	// the first check tops it up.
	grew, atCeiling, err := d.growIfNearlyFull()
	if err != nil || !grew || atCeiling {
		t.Fatalf("growIfNearlyFull on small fresh db = grew=%v ceiling=%v err=%v, want top-up growth", grew, atCeiling, err)
	}
	fsBytes, err := d.fsSize()
	if err != nil {
		t.Fatal(err)
	}
	if fsBytes <= 128<<20 {
		t.Errorf("filesystem still %d bytes after grow", fsBytes)
	}

	// Now roomy: no further growth.
	if grew, atCeiling, err := d.growIfNearlyFull(); err != nil || grew || atCeiling {
		t.Errorf("growIfNearlyFull after top-up = grew=%v ceiling=%v err=%v, want no-op", grew, atCeiling, err)
	}

	// Nearly fill the filesystem (down to ~30MB free), then the check must
	// grow it again.
	var st syscall.Statfs_t
	if err := syscall.Statfs(d.MountDir(), &st); err != nil {
		t.Fatal(err)
	}
	fillerSize := int64(st.Bavail)*int64(st.Bsize) - 30<<20
	if fillerSize <= 0 {
		t.Fatalf("filesystem unexpectedly full already: %d bytes available", int64(st.Bavail)*int64(st.Bsize))
	}
	filler := filepath.Join(d.MountDir(), "filler")
	if err := os.WriteFile(filler, make([]byte, fillerSize), 0o600); err != nil {
		t.Fatalf("writing filler: %v", err)
	}
	before := fsBytes
	grew, atCeiling, err = d.growIfNearlyFull()
	if err != nil || !grew || atCeiling {
		t.Fatalf("growIfNearlyFull on full db = grew=%v ceiling=%v err=%v, want growth", grew, atCeiling, err)
	}
	if fsBytes, err = d.fsSize(); err != nil || fsBytes <= before {
		t.Errorf("filesystem did not grow: %d -> %d (err=%v)", before, fsBytes, err)
	}
	os.Remove(filler)

	// Closing sheds the ceiling: file size == filesystem size.
	if err := d.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}
	fi, err = os.Stat(d.Image)
	if err != nil {
		t.Fatal(err)
	}
	fsBytes, err = d.fsSize()
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != fsBytes {
		t.Errorf("at rest: file %d bytes, filesystem %d bytes; ceiling not shed", fi.Size(), fsBytes)
	}
}
