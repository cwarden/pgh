package db

import (
	"fmt"
	"os"
	"os/exec"
)

// Resize grows or shrinks the image to size bytes using resize2fs. The
// database must be closed. Returns whether anything changed (a no-op when
// the image is already the requested size).
func (d *DB) Resize(size int64) (bool, error) {
	if size < MinImageSize {
		return false, fmt.Errorf("size %s is below the minimum %s", FormatSize(size), FormatSize(MinImageSize))
	}
	unlock, err := d.lock()
	if err != nil {
		return false, err
	}
	defer unlock()

	fi, err := os.Stat(d.Image)
	if err != nil {
		return false, err
	}
	if fi.Size() == size {
		return false, nil
	}
	if st, err := d.state(); err != nil {
		return false, err
	} else if st != stateClosed {
		return false, fmt.Errorf("%s is open; stop it before resizing", d.Image)
	}

	// resize2fs refuses to touch a filesystem that hasn't just been checked.
	if err := d.fsck(); err != nil {
		return false, err
	}
	resize2fs, err := findTool("resize2fs")
	if err != nil {
		return false, err
	}
	if size > fi.Size() {
		// Grow: enlarge the file, then let resize2fs fill it.
		if err := os.Truncate(d.Image, size); err != nil {
			return false, err
		}
		if out, err := runCmd(resize2fs, d.Image); err != nil {
			os.Truncate(d.Image, fi.Size())
			return false, fmt.Errorf("resize2fs failed: %v: %s", err, out)
		}
		return true, nil
	}
	// Shrink: reduce the filesystem first, then the file. The size is
	// passed in KiB so it is independent of the filesystem's block size.
	if out, err := runCmd(resize2fs, d.Image, fmt.Sprintf("%dK", size/1024)); err != nil {
		return false, fmt.Errorf("resize2fs failed: %v: %s", err, out)
	}
	return true, os.Truncate(d.Image, size)
}

// fsck runs e2fsck in preen mode. Exit codes 0 (clean) and 1 (errors
// corrected) are success.
func (d *DB) fsck() error {
	e2fsck, err := findTool("e2fsck")
	if err != nil {
		return err
	}
	out, err := exec.Command(e2fsck, "-f", "-p", d.Image).CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() <= 1 {
			return nil
		}
		return fmt.Errorf("e2fsck failed: %v: %s", err, out)
	}
	return nil
}
