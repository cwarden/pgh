package db

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The pack backend supports platforms that cannot mount ext4 at all (macOS,
// and Linux with PGH_MOUNT=pack): on open, the image's contents are
// extracted to a native directory with debugfs; PostgreSQL then runs at
// native filesystem speed; on close, the directory is packed back into a
// fresh ext4 image with mke2fs -d and atomically renamed over the file.
//
// The trade-off versus mounting: the database file is stale while open, and
// open/close cost time proportional to the database size.

// openPacked extracts the image into MountDir, or creates an empty
// directory when the image does not exist yet (a brand-new database, packed
// for the first time right after initdb).
func (d *DB) openPacked() error {
	if err := os.MkdirAll(d.MountDir(), 0o700); err != nil {
		return err
	}
	if !d.ImageExists() {
		return nil
	}
	debugfs, err := findTool("debugfs")
	if err != nil {
		return err
	}
	// debugfs exits 0 even on failure, so its output is the only signal.
	out, err := runCmd(debugfs, "-R", fmt.Sprintf(`rdump / "%s"`, d.MountDir()), d.Image)
	if err == nil {
		if msg := debugfsErrors(out); msg != "" {
			err = fmt.Errorf("debugfs rdump: %s", msg)
		}
	}
	if err != nil {
		os.RemoveAll(d.MountDir())
		return fmt.Errorf("unpacking %s: %v", d.Image, err)
	}
	return nil
}

// debugfsErrors extracts failure lines from debugfs output, ignoring the
// version banner and the expected complaint about chowning root-owned
// entries (lost+found) as a regular user.
func debugfsErrors(out string) string {
	var errs []string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "while changing ownership of") {
			continue
		}
		if strings.HasPrefix(line, "rdump: ") || strings.Contains(line, "while trying to open") {
			errs = append(errs, line)
		}
	}
	return strings.Join(errs, "; ")
}

// packImage builds a fresh ext4 image from MountDir's contents and
// atomically replaces the database file. The image is sized to hold the
// content with headroom, and never shrinks below minSize or the existing
// image's size.
func (d *DB) packImage(minSize int64) error {
	mkfs, err := findMkfs()
	if err != nil {
		return err
	}
	content, err := dirSize(d.MountDir())
	if err != nil {
		return err
	}
	size := max(content+content/5, minSize, MinImageSize)
	if fi, err := os.Stat(d.Image); err == nil {
		size = max(size, fi.Size())
	}
	tmp := d.Image + ".packing"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = f.Truncate(size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if out, err := runCmd(mkfs, "-q", "-F", "-t", "ext4", "-m", "0",
		"-E", fmt.Sprintf("root_owner=%d:%d", os.Getuid(), os.Getgid()),
		"-d", d.MountDir(), tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("packing %s: %v: %s", d.Image, err, out)
	}
	return os.Rename(tmp, d.Image)
}

// closePacked packs the directory back into the image and removes the
// directory. On pack failure the directory is left in place so no data is
// lost.
func (d *DB) closePacked() error {
	if err := d.packImage(0); err != nil {
		return err
	}
	return os.RemoveAll(d.MountDir())
}

// dirSize returns the total size of all regular files under dir, plus a
// per-entry allowance for metadata.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		total += 4096
		if entry.Type().IsRegular() {
			if fi, err := entry.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, err
}
