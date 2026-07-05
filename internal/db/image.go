package db

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// findTool locates an external command on PATH, falling back to the sbin
// directories often absent from non-root PATHs and to Homebrew's e2fsprogs
// on macOS.
func findTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range []string{
		"/usr/sbin", "/sbin", "/usr/local/sbin",
		"/opt/homebrew/sbin",
		"/opt/homebrew/opt/e2fsprogs/sbin", "/opt/homebrew/opt/e2fsprogs/bin",
		"/usr/local/opt/e2fsprogs/sbin", "/usr/local/opt/e2fsprogs/bin",
	} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found; please install it", name)
}

// findMkfs locates the ext4 formatter under either of its names.
func findMkfs() (string, error) {
	if p, err := findTool("mke2fs"); err == nil {
		return p, nil
	}
	return findTool("mkfs.ext4")
}

// ImageExists reports whether the database file exists.
func (d *DB) ImageExists() bool {
	fi, err := os.Stat(d.Image)
	return err == nil && fi.Mode().IsRegular()
}

// CreateImage creates a sparse file of the given size and formats it as ext4
// owned by the current user, so it can be used through fuse2fs without root.
func (d *DB) CreateImage(size int64) error {
	if size < MinImageSize {
		return fmt.Errorf("size %s is below the minimum %s", FormatSize(size), FormatSize(MinImageSize))
	}
	mkfs, err := findMkfs()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(d.Image, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		os.Remove(d.Image)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(d.Image)
		return err
	}
	cmd := exec.Command(mkfs, "-q", "-F", "-t", "ext4", "-m", "0",
		"-E", fmt.Sprintf("root_owner=%d:%d", os.Getuid(), os.Getgid()),
		d.Image)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(d.Image)
		return fmt.Errorf("mkfs.ext4 failed: %v: %s", err, out)
	}
	return nil
}
