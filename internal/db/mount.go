package db

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// pgh mounts images through the first strategy that works:
//
//  1. udisks: a kernel loop device plus an in-kernel ext4 mount, set up via
//     udisksctl. Polkit typically authorizes this for local desktop sessions
//     without root. udisks chooses the mountpoint (under /media or
//     /run/media), so MountDir becomes a symlink to it. Performance is
//     near-native.
//  2. fuse2fs: a userspace ext4 driver. Works anywhere FUSE does, but is
//     single-threaded and roughly 3x slower. MountDir is a real directory.
//
// Set PGH_MOUNT=fuse2fs to skip udisks.

// Mounted reports whether the image's filesystem is currently mounted at
// (or via) MountDir. A stale FUSE mount (fuse2fs died) is cleaned up and
// reported as not mounted.
func (d *DB) Mounted() (bool, error) {
	target, err := d.resolveMountDir()
	if err != nil {
		return false, err
	}
	if target == "" {
		return false, nil
	}
	entry, err := findMount(target)
	if err != nil {
		return false, err
	}
	return entry != nil, nil
}

// MountBackend describes how the image is mounted: "kernel" (udisks loop
// mount), "fuse2fs", or "" when not mounted.
func (d *DB) MountBackend() string {
	target, err := d.resolveMountDir()
	if err != nil || target == "" {
		return ""
	}
	entry, err := findMount(target)
	if err != nil || entry == nil {
		return ""
	}
	if strings.HasPrefix(entry.fstype, "fuse") {
		return "fuse2fs"
	}
	return "kernel"
}

// resolveMountDir canonicalizes MountDir, following the udisks symlink if
// present. It returns "" if the path does not exist, and cleans up a stale
// FUSE mount (ENOTCONN) as a side effect.
func (d *DB) resolveMountDir() (string, error) {
	target, err := filepath.EvalSymlinks(d.MountDir())
	if errors.Is(err, syscall.ENOTCONN) {
		// fuse2fs is gone but the mount table entry remains; detach it.
		if err := d.unmountFuse(); err != nil {
			return "", fmt.Errorf("stale mount at %s: %v", d.MountDir(), err)
		}
		return "", nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return target, nil
}

// Mount mounts the image, preferring a kernel loop mount via udisks and
// falling back to fuse2fs.
func (d *DB) Mount() error {
	if err := d.ensureStateDir(); err != nil {
		return err
	}
	var udisksErr error
	if os.Getenv("PGH_MOUNT") != "fuse2fs" {
		udisksErr = d.mountUdisks()
		if udisksErr == nil {
			return nil
		}
	}
	if err := d.mountFuse2fs(); err != nil {
		if udisksErr != nil {
			return fmt.Errorf("udisks: %v; fuse2fs: %v", udisksErr, err)
		}
		return err
	}
	return nil
}

// Unmount detaches the filesystem. It is a no-op if nothing is mounted.
func (d *DB) Unmount() error {
	if fi, err := os.Lstat(d.MountDir()); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return d.unmountUdisks()
	}
	return d.unmountFuse()
}

// --- udisks backend ---

func (d *DB) mountUdisks() error {
	udisksctl, err := exec.LookPath("udisksctl")
	if err != nil {
		return err
	}
	// Reuse an existing loop device for this image rather than creating a
	// second one: two writable loop devices over the same file would let the
	// filesystem be mounted twice and corrupted.
	dev, err := loopDeviceFor(d.Image)
	if err != nil {
		return err
	}
	createdLoop := false
	if dev == "" {
		out, err := runCmd(udisksctl, "loop-setup", "-f", d.Image, "--no-user-interaction")
		if err != nil {
			return err
		}
		dev = matchOne(`as (/dev/loop\S+?)\.?$`, out)
		if dev == "" {
			return fmt.Errorf("could not parse loop device from udisksctl output: %s", out)
		}
		createdLoop = true
	}
	target, err := mountpointOf(dev)
	if err != nil {
		return err
	}
	if target == "" {
		out, err := runCmd(udisksctl, "mount", "-b", dev, "--no-user-interaction")
		if err != nil {
			// The desktop may have auto-mounted the device in the meantime.
			if target, _ = mountpointOf(dev); target == "" {
				if createdLoop {
					runCmd(udisksctl, "loop-delete", "-b", dev, "--no-user-interaction")
				}
				return err
			}
		} else {
			target = matchOne(` at (.+?)\.?$`, out)
		}
	}
	if target == "" {
		return fmt.Errorf("could not determine mountpoint of %s", dev)
	}
	// Point the stable mnt path at the mountpoint udisks chose.
	os.Remove(d.MountDir())
	return os.Symlink(target, d.MountDir())
}

func (d *DB) unmountUdisks() error {
	dev, err := loopDeviceFor(d.Image)
	if err != nil {
		return err
	}
	if dev == "" {
		// losetup matches by backing-file path, which can miss when the
		// image has been deleted; fall back to the mount table.
		if target, _ := d.resolveMountDir(); target != "" {
			if entry, _ := findMount(target); entry != nil {
				dev = entry.dev
			}
		}
	}
	if dev != "" {
		udisksctl, err := exec.LookPath("udisksctl")
		if err != nil {
			return err
		}
		if target, _ := mountpointOf(dev); target != "" {
			if _, err := runCmd(udisksctl, "unmount", "-b", dev, "--no-user-interaction"); err != nil {
				return err
			}
		}
		// udisks usually auto-clears the loop device on unmount; ignore
		// failures from racing that.
		runCmd(udisksctl, "loop-delete", "-b", dev, "--no-user-interaction")
	}
	os.Remove(d.MountDir())
	return nil
}

// loopDeviceFor returns the loop device backed by the given file, or "".
func loopDeviceFor(image string) (string, error) {
	losetup, err := findTool("losetup")
	if err != nil {
		return "", err
	}
	out, err := runCmd(losetup, "-n", "-O", "NAME", "-j", image)
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line, nil
		}
	}
	return "", nil
}

// --- fuse2fs backend ---

func (d *DB) mountFuse2fs() error {
	fuse2fs, err := findTool("fuse2fs")
	if err != nil {
		return err
	}
	// A previous udisks session may have left mnt as a (dangling) symlink.
	if fi, err := os.Lstat(d.MountDir()); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		os.Remove(d.MountDir())
	}
	if err := os.MkdirAll(d.MountDir(), 0o700); err != nil {
		return err
	}
	if out, err := runCmd(fuse2fs, d.Image, d.MountDir()); err != nil {
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

func (d *DB) unmountFuse() error {
	fusermount, err := findTool("fusermount3")
	if err != nil {
		fusermount, err = findTool("fusermount")
		if err != nil {
			return err
		}
	}
	if out, err := runCmd(fusermount, "-u", "-z", d.MountDir()); err != nil {
		if entry, merr := findMount(d.MountDir()); merr == nil && entry == nil {
			return nil
		}
		return fmt.Errorf("fusermount failed: %v: %s", err, out)
	}
	return nil
}

// --- mount table ---

type mountEntry struct {
	dev    string
	target string
	fstype string
}

func readMounts() ([]mountEntry, error) {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	var entries []mountEntry
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		entries = append(entries, mountEntry{
			dev:    unescapeMountPath(fields[0]),
			target: unescapeMountPath(fields[1]),
			fstype: fields[2],
		})
	}
	return entries, nil
}

// findMount returns the mount table entry whose mountpoint is target, or nil.
func findMount(target string) (*mountEntry, error) {
	entries, err := readMounts()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].target == target {
			return &entries[i], nil
		}
	}
	return nil, nil
}

func mountpointOf(dev string) (string, error) {
	entries, err := readMounts()
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.dev == dev {
			return e.target, nil
		}
	}
	return "", nil
}

// unescapeMountPath decodes the octal escapes (\040 for space, etc.) used in
// /proc/self/mounts.
func unescapeMountPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) &&
			s[i+1] >= '0' && s[i+1] <= '3' &&
			s[i+2] >= '0' && s[i+2] <= '7' &&
			s[i+3] >= '0' && s[i+3] <= '7' {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// --- helpers ---

var matchOneCache = map[string]*regexp.Regexp{}

// matchOne applies an anchored-at-line-end regexp to the last line of out
// and returns the first capture group, or "".
func matchOne(pattern, out string) string {
	re := matchOneCache[pattern]
	if re == nil {
		re = regexp.MustCompile(pattern)
		matchOneCache[pattern] = re
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	m := re.FindStringSubmatch(lines[len(lines)-1])
	if m == nil {
		return ""
	}
	return m[1]
}

// runCmd runs a command and returns its trimmed combined output.
func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("%s %s: %v: %s", filepath.Base(name), strings.Join(args, " "), err, s)
	}
	return s, nil
}
