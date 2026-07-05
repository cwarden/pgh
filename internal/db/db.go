// Package db manages single-file PostgreSQL databases: an ext4 filesystem
// inside a regular file, mounted without root via fuse2fs, containing a
// PostgreSQL data directory.
package db

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DB represents one database file and its derived runtime locations.
type DB struct {
	// Image is the absolute path to the database file (the ext4 image).
	Image string
	// StateDir holds the mountpoint, socket directory, and lock for this image.
	StateDir string
	// BinDir is the PostgreSQL binary directory. Empty means discover on demand.
	BinDir string
}

// New returns a DB for the given image path. The path does not need to exist yet.
func New(image string) (*DB, error) {
	abs, err := filepath.Abs(image)
	if err != nil {
		return nil, err
	}
	// Resolve symlinks in existing ancestors so the same file always maps to
	// the same state dir.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	} else if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(dir, filepath.Base(abs))
	}
	return &DB{
		Image:    abs,
		StateDir: filepath.Join(StateBaseDir(), stateName(abs)),
	}, nil
}

// StateBaseDir is the directory under which per-database state dirs live.
func StateBaseDir() string {
	if d := os.Getenv("PGH_STATE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "pgh")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("pgh-%d", os.Getuid()))
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// stateName builds a short, unique, human-recognizable directory name for an
// image path: sanitized basename plus a hash of the full path.
func stateName(abs string) string {
	base := filepath.Base(abs)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = unsafeChars.ReplaceAllString(base, "-")
	if len(base) > 32 {
		base = base[:32]
	}
	sum := sha256.Sum256([]byte(abs))
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// MountDir is where the image's filesystem is mounted.
func (d *DB) MountDir() string { return filepath.Join(d.StateDir, "mnt") }

// DataDir is the PostgreSQL data directory inside the mounted filesystem.
func (d *DB) DataDir() string { return filepath.Join(d.MountDir(), "data") }

// SockDir is the Unix socket directory. It lives in the state dir (not the
// image) to keep the socket path short.
func (d *DB) SockDir() string { return filepath.Join(d.StateDir, "sock") }

// LogFile is the server log, kept inside the image so it travels with it.
func (d *DB) LogFile() string { return filepath.Join(d.MountDir(), "postgres.log") }

// sourceFile records which image a state dir belongs to, for `pgh status`.
func (d *DB) sourceFile() string { return filepath.Join(d.StateDir, "source") }

// ensureStateDir creates the state dir, socket dir, and source marker.
func (d *DB) ensureStateDir() error {
	if err := os.MkdirAll(d.SockDir(), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(d.MountDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(d.sourceFile(), []byte(d.Image+"\n"), 0o600)
}

// All returns a DB for every state dir under the base dir, mapping back to
// image paths via the source markers.
func All() ([]*DB, error) {
	entries, err := os.ReadDir(StateBaseDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var dbs []*DB
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		stateDir := filepath.Join(StateBaseDir(), e.Name())
		src, err := os.ReadFile(filepath.Join(stateDir, "source"))
		if err != nil {
			continue
		}
		dbs = append(dbs, &DB{
			Image:    strings.TrimSpace(string(src)),
			StateDir: stateDir,
		})
	}
	return dbs, nil
}
