package db

import (
	"path/filepath"
	"testing"
)

func TestUnescapeMountPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/plain/path", "/plain/path"},
		{`/with\040space`, "/with space"},
		{`/tab\011here`, "/tab\there"},
		{`/back\134slash`, `/back\slash`},
		{`/media/user/my\040db\040(test)`, "/media/user/my db (test)"},
		{`/trailing\04`, `/trailing\04`},
	}
	for _, c := range cases {
		if got := unescapeMountPath(c.in); got != c.want {
			t.Errorf("unescapeMountPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatchOne(t *testing.T) {
	out := "Mapped file /x/y.pdb as /dev/loop8."
	if got := matchOne(`as (/dev/loop\S+?)\.?$`, out); got != "/dev/loop8" {
		t.Errorf("loop device parse = %q", got)
	}
	out = "Mounted /dev/loop8 at /media/user/abc123"
	if got := matchOne(` at (.+?)\.?$`, out); got != "/media/user/abc123" {
		t.Errorf("mountpoint parse = %q", got)
	}
	// Older udisks appends a period.
	out = "Mounted /dev/loop8 at /media/user/abc123."
	if got := matchOne(` at (.+?)\.?$`, out); got != "/media/user/abc123" {
		t.Errorf("mountpoint parse with period = %q", got)
	}
	if got := matchOne(`as (/dev/loop\S+?)\.?$`, "unrelated"); got != "" {
		t.Errorf("no-match parse = %q, want empty", got)
	}
}

func TestFindMountRoot(t *testing.T) {
	entry, err := findMount("/")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("/ not found in mount table")
	}
	if entry.fstype == "" {
		t.Error("/ has empty fstype")
	}
}

// TestMountFuse2fsForced exercises the fuse2fs backend explicitly via
// PGH_MOUNT, independent of whether udisks is authorized on this machine.
func TestMountFuse2fsForced(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if _, err := findTool("fuse2fs"); err != nil {
		t.Skip("fuse2fs not available")
	}
	if _, err := findTool("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available")
	}
	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", filepath.Join(base, "state"))
	t.Setenv("PGH_MOUNT", "fuse2fs")

	d, err := New(filepath.Join(base, "fuse.pdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateImage(MinImageSize); err != nil {
		t.Fatal(err)
	}
	if err := d.ensureStateDir(); err != nil {
		t.Fatal(err)
	}
	if err := d.Mount(); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { d.Unmount() })
	if backend := d.MountBackend(); backend != "fuse2fs" {
		t.Errorf("MountBackend() = %q, want fuse2fs", backend)
	}
	if err := d.Unmount(); err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	if mounted, err := d.Mounted(); err != nil || mounted {
		t.Errorf("after Unmount: mounted=%v err=%v", mounted, err)
	}
}
