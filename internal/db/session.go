package db

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// UpOptions controls Up.
type UpOptions struct {
	// Size for a newly created image; ignored if the image exists.
	Size int64
	// Port enables TCP on 127.0.0.1:Port in addition to the Unix socket.
	Port int
	// Durable makes commits wait for the WAL to reach disk (PostgreSQL's
	// default). Off by default: see Start.
	Durable bool
}

// Up ensures the database is running: it creates the image if missing,
// mounts it if unmounted, runs initdb on first use, and starts the server if
// it is not already up. It returns connection info and whether this call
// started the server (as opposed to finding it already running).
func (d *DB) Up(opts UpOptions) (info *ConnInfo, started bool, err error) {
	unlock, err := d.lock()
	if err != nil {
		return nil, false, err
	}
	defer unlock()

	if !d.ImageExists() {
		size := opts.Size
		if size == 0 {
			size = DefaultImageSize
		}
		if err := d.CreateImage(size); err != nil {
			return nil, false, err
		}
	}
	mounted, err := d.Mounted()
	if err != nil {
		return nil, false, err
	}
	if !mounted {
		if err := d.Mount(); err != nil {
			return nil, false, err
		}
	}
	if !d.Initialized() {
		if err := d.InitDB(); err != nil {
			d.cleanupAfterFailure(mounted)
			return nil, false, err
		}
	}
	info, err = d.Running()
	if err != nil {
		return nil, false, err
	}
	if info == nil {
		if err := d.Start(opts.Port, opts.Durable); err != nil {
			d.cleanupAfterFailure(mounted)
			return nil, false, err
		}
		started = true
		info, err = d.Running()
		if err == nil && info == nil {
			err = fmt.Errorf("server started but postmaster.pid not found in %s", d.DataDir())
		}
		if err != nil {
			return nil, false, err
		}
	}
	return info, started, nil
}

// cleanupAfterFailure unmounts the image if this call mounted it, so a
// failed Up doesn't leave a stray fuse2fs process behind.
func (d *DB) cleanupAfterFailure(wasMounted bool) {
	if !wasMounted {
		d.Unmount()
	}
}

// Down stops the server if it is running and unmounts the image if it is
// mounted. It is idempotent.
func (d *DB) Down() error {
	unlock, err := d.lock()
	if err != nil {
		return err
	}
	defer unlock()

	info, err := d.Running()
	if err != nil {
		return err
	}
	if info != nil {
		if err := d.Stop(); err != nil {
			return err
		}
	}
	mounted, err := d.Mounted()
	if err != nil {
		return err
	}
	if mounted {
		if err := d.Unmount(); err != nil {
			return err
		}
	}
	return nil
}

// Cleanup removes all runtime state for an image that no longer exists:
// it stops the server and unmounts if anything is still up (a mounted
// filesystem keeps a deleted image's inode alive), then deletes the state
// dir. It is a no-op if the image still exists.
func (d *DB) Cleanup() error {
	if d.ImageExists() {
		return nil
	}
	if err := d.Down(); err != nil {
		return err
	}
	unlock, err := d.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if d.ImageExists() {
		// The image reappeared (e.g. a concurrent pgh is creating it).
		return nil
	}
	return os.RemoveAll(d.StateDir)
}

// lock takes an exclusive flock on the state dir so concurrent pgh
// invocations don't race mounting or starting the same image.
func (d *DB) lock() (unlock func(), err error) {
	if err := os.MkdirAll(d.StateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(d.StateDir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %v", d.StateDir, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
