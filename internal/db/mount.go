package db

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Mounted reports whether the image's filesystem is currently mounted at
// MountDir. A stale FUSE mount (fuse2fs died) is cleaned up and reported as
// not mounted.
func (d *DB) Mounted() (bool, error) {
	var st, parent syscall.Stat_t
	err := syscall.Stat(d.MountDir(), &st)
	if errors.Is(err, syscall.ENOTCONN) {
		// fuse2fs is gone but the mount table entry remains; detach it.
		if err := d.Unmount(); err != nil {
			return false, fmt.Errorf("stale mount at %s: %v", d.MountDir(), err)
		}
		return false, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := syscall.Stat(filepath.Dir(d.MountDir()), &parent); err != nil {
		return false, err
	}
	return st.Dev != parent.Dev, nil
}

// Mount mounts the image at MountDir via fuse2fs.
func (d *DB) Mount() error {
	fuse2fs, err := findTool("fuse2fs")
	if err != nil {
		return err
	}
	if err := d.ensureStateDir(); err != nil {
		return err
	}
	cmd := exec.Command(fuse2fs, d.Image, d.MountDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fuse2fs failed: %v: %s", err, out)
	}
	mounted, err := d.Mounted()
	if err != nil {
		return err
	}
	if !mounted {
		return fmt.Errorf("fuse2fs reported success but %s is not mounted", d.MountDir())
	}
	return nil
}

// Unmount detaches the filesystem. It is a no-op if nothing is mounted.
func (d *DB) Unmount() error {
	fusermount, err := findTool("fusermount3")
	if err != nil {
		fusermount, err = findTool("fusermount")
		if err != nil {
			return err
		}
	}
	cmd := exec.Command(fusermount, "-u", "-z", d.MountDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		if mounted, merr := isMountpoint(d.MountDir()); merr == nil && !mounted {
			return nil
		}
		return fmt.Errorf("fusermount failed: %v: %s", err, out)
	}
	return nil
}

// isMountpoint is a plain device-number check without the stale-mount
// handling of Mounted, for use inside Unmount.
func isMountpoint(dir string) (bool, error) {
	var st, parent syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := syscall.Stat(filepath.Dir(dir), &parent); err != nil {
		return false, err
	}
	return st.Dev != parent.Dev, nil
}
