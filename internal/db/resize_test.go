package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	for _, tool := range []string{"resize2fs", "e2fsck"} {
		if _, err := findTool(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	if _, err := findMkfs(); err != nil {
		t.Skip("mke2fs not available")
	}

	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", filepath.Join(base, "state"))
	d, err := New(filepath.Join(base, "resize.pdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateImage(128 << 20); err != nil {
		t.Fatal(err)
	}

	if resized, err := d.Resize(128 << 20); err != nil || resized {
		t.Errorf("same-size resize: resized=%v err=%v, want no-op", resized, err)
	}

	if resized, err := d.Resize(256 << 20); err != nil || !resized {
		t.Fatalf("grow: resized=%v err=%v", resized, err)
	}
	if fi, _ := os.Stat(d.Image); fi.Size() != 256<<20 {
		t.Errorf("after grow: file size %d, want %d", fi.Size(), int64(256<<20))
	}
	if err := d.fsck(); err != nil {
		t.Errorf("filesystem not clean after grow: %v", err)
	}

	if resized, err := d.Resize(96 << 20); err != nil || !resized {
		t.Fatalf("shrink: resized=%v err=%v", resized, err)
	}
	if fi, _ := os.Stat(d.Image); fi.Size() != 96<<20 {
		t.Errorf("after shrink: file size %d, want %d", fi.Size(), int64(96<<20))
	}
	if err := d.fsck(); err != nil {
		t.Errorf("filesystem not clean after shrink: %v", err)
	}

	if _, err := d.Resize(1 << 20); err == nil {
		t.Error("resize below minimum should fail")
	}
}

func TestResizeRefusesWhileOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if _, err := findTool("fuse2fs"); err != nil {
		t.Skip("fuse2fs not available")
	}
	for _, tool := range []string{"resize2fs", "e2fsck"} {
		if _, err := findTool(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	if _, err := findMkfs(); err != nil {
		t.Skip("mke2fs not available")
	}

	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", filepath.Join(base, "state"))
	t.Setenv("PGH_MOUNT", "fuse2fs")
	d, err := New(filepath.Join(base, "open.pdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateImage(MinImageSize); err != nil {
		t.Fatal(err)
	}
	if err := d.Mount(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Unmount() })

	if _, err := d.Resize(256 << 20); err == nil {
		t.Error("resizing an open database should fail")
	}
}
