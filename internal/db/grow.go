package db

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Online growth (kernel mounts only): the loop device's capacity is fixed at
// setup time and LOOP_SET_CAPACITY is root-only, so pgh gives the device its
// ceiling up front — the sparse image file is truncated up by a large
// headroom just before loop-setup, costing no disk. While mounted, the
// filesystem can then be grown into that ceiling with udisks'
// Filesystem.Resize (polkit-authorized like the mount itself). On unmount
// the file is truncated back down to the filesystem's actual size.
//
// A watcher process (pgh __watch, spawned automatically for kernel mounts)
// polls free space and grows the filesystem when it is nearly full.

// defaultGrowHeadroom is how much room for online growth a kernel-mounted
// database gets. Sparse, so it costs address space, not disk.
const defaultGrowHeadroom = 64 << 30

// growHeadroom returns the online-growth ceiling headroom. Set
// PGH_GROW_HEADROOM to override (e.g. 16G), or to 0/off to disable online
// growth.
func growHeadroom() int64 {
	v := os.Getenv("PGH_GROW_HEADROOM")
	if v == "" {
		return defaultGrowHeadroom
	}
	if v == "0" || strings.EqualFold(v, "off") {
		return 0
	}
	n, err := ParseSize(v)
	if err != nil {
		return defaultGrowHeadroom
	}
	return n
}

// fsSize returns the size in bytes of the ext4 filesystem inside the image
// (which can be smaller than the file while a growth ceiling is in place).
func (d *DB) fsSize() (int64, error) {
	dumpe2fs, err := findTool("dumpe2fs")
	if err != nil {
		return 0, err
	}
	out, err := runCmd(dumpe2fs, "-h", d.Image)
	if err != nil {
		return 0, fmt.Errorf("dumpe2fs failed: %v", err)
	}
	return parseFsSize(out)
}

var (
	blockCountRe = regexp.MustCompile(`(?m)^Block count:\s+(\d+)`)
	blockSizeRe  = regexp.MustCompile(`(?m)^Block size:\s+(\d+)`)
)

func parseFsSize(out string) (int64, error) {
	count := blockCountRe.FindStringSubmatch(out)
	size := blockSizeRe.FindStringSubmatch(out)
	if count == nil || size == nil {
		return 0, fmt.Errorf("could not find block count/size in dumpe2fs output")
	}
	c, err := strconv.ParseInt(count[1], 10, 64)
	if err != nil {
		return 0, err
	}
	s, err := strconv.ParseInt(size[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return c * s, nil
}

// setGrowthCeiling truncates the image to the filesystem size plus headroom
// so the upcoming loop device has capacity to grow into.
func (d *DB) setGrowthCeiling() {
	headroom := growHeadroom()
	if headroom == 0 {
		return
	}
	fsBytes, err := d.fsSize()
	if err != nil {
		return
	}
	if fi, err := os.Stat(d.Image); err == nil && fi.Size() != fsBytes+headroom {
		os.Truncate(d.Image, fsBytes+headroom)
	}
}

// dropGrowthCeiling truncates the image back down to the filesystem's size.
func (d *DB) dropGrowthCeiling() {
	fsBytes, err := d.fsSize()
	if err != nil {
		return
	}
	if fi, err := os.Stat(d.Image); err == nil && fi.Size() > fsBytes {
		os.Truncate(d.Image, fsBytes)
	}
}

// loopCapacity returns the size in bytes of the loop device backing the
// mounted image.
func (d *DB) loopCapacity() (int64, error) {
	dev, err := loopDeviceFor(d.Image)
	if err != nil {
		return 0, err
	}
	if dev == "" {
		return 0, fmt.Errorf("no loop device for %s", d.Image)
	}
	data, err := os.ReadFile(filepath.Join("/sys/block", filepath.Base(dev), "size"))
	if err != nil {
		return 0, err
	}
	sectors, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	return sectors * 512, nil
}

// GrowOnline grows the mounted filesystem to target bytes via udisks.
func (d *DB) GrowOnline(target int64) error {
	dev, err := loopDeviceFor(d.Image)
	if err != nil {
		return err
	}
	if dev == "" {
		return fmt.Errorf("no loop device for %s", d.Image)
	}
	busctl, err := exec.LookPath("busctl")
	if err != nil {
		return err
	}
	if out, err := runCmd(busctl, "call", "org.freedesktop.UDisks2",
		"/org/freedesktop/UDisks2/block_devices/"+filepath.Base(dev),
		"org.freedesktop.UDisks2.Filesystem", "Resize",
		"ta{sv}", strconv.FormatInt(target, 10), "0"); err != nil {
		return fmt.Errorf("online resize failed: %v: %s", err, out)
	}
	return nil
}

// growIfNearlyFull grows the filesystem when free space is below 25% (or
// 64MB, whichever is larger), adding max(current size, 512MB) up to the
// loop device's capacity. The cushions are deliberately generous: growth is
// poll-based, and a fast bulk load can outrun a poller with a small margin
// (PostgreSQL PANICs when the WAL hits ENOSPC; data files get a plain
// transaction error). A side effect is that a small fresh database is
// topped up on the watcher's first check, before the first burst of
// writes. It reports whether it grew and whether the ceiling is reached.
func (d *DB) growIfNearlyFull() (grew, atCeiling bool, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(d.MountDir(), &st); err != nil {
		return false, false, err
	}
	total := int64(st.Blocks) * int64(st.Bsize)
	avail := int64(st.Bavail) * int64(st.Bsize)
	if avail >= max(64<<20, total/4) {
		return false, false, nil
	}
	capacity, err := d.loopCapacity()
	if err != nil {
		return false, false, err
	}
	target := min(total+max(total, 512<<20), capacity) &^ 4095
	if target <= total {
		return false, true, nil
	}
	if err := d.GrowOnline(target); err != nil {
		return false, false, err
	}
	return true, false, nil
}

// Watch polls a kernel-mounted database and grows its filesystem online
// when nearly full. It returns when the database is closed, the server
// stops, or growth fails repeatedly.
func (d *DB) Watch(interval time.Duration, logf func(format string, args ...any)) {
	warnedCeiling := false
	failures := 0
	for {
		if st, err := d.state(); err != nil || st != stateKernel {
			return
		}
		if info, err := d.Running(); err != nil || info == nil {
			return
		}
		grew, atCeiling, err := d.growIfNearlyFull()
		switch {
		case err != nil:
			failures++
			logf("growing %s: %v", d.Image, err)
			if failures >= 5 {
				logf("giving up after %d failures", failures)
				return
			}
		case grew:
			failures = 0
			warnedCeiling = false
			if fsBytes, err := d.fsSize(); err == nil {
				logf("grew %s to %s", d.Image, FormatSize(fsBytes))
			}
		case atCeiling && !warnedCeiling:
			warnedCeiling = true
			logf("%s is nearly full and at its growth ceiling; raise PGH_GROW_HEADROOM and restart to grow further", d.Image)
		}
		time.Sleep(interval)
	}
}

// --- watcher process management ---

func (d *DB) watcherPIDFile() string { return filepath.Join(d.StateDir, "watch.pid") }

// WatchLogFile is where the background watcher's output goes.
func (d *DB) WatchLogFile() string { return filepath.Join(d.StateDir, "watch.log") }

// RecordWatcherPID marks the current process as this database's watcher.
func (d *DB) RecordWatcherPID() error {
	return os.WriteFile(d.watcherPIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

// WatcherAlive reports whether a watcher process is running for this
// database.
func (d *DB) WatcherAlive() bool {
	pid, err := readPIDFile(d.watcherPIDFile())
	if err != nil {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// StopWatcher terminates the watcher process, if any.
func (d *DB) StopWatcher() {
	pid, err := readPIDFile(d.watcherPIDFile())
	if err == nil {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	os.Remove(d.watcherPIDFile())
}

func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}
