package db

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// FindBinDir locates the PostgreSQL server binaries (initdb, pg_ctl,
// postgres), which are often not on PATH on Debian/Ubuntu.
func FindBinDir() (string, error) {
	if d := os.Getenv("PGH_BINDIR"); d != "" {
		return d, nil
	}
	if pgConfig, err := exec.LookPath("pg_config"); err == nil {
		if out, err := exec.Command(pgConfig, "--bindir").Output(); err == nil {
			dir := strings.TrimSpace(string(out))
			if hasServerBinaries(dir) {
				return dir, nil
			}
		}
	}
	if p, err := exec.LookPath("initdb"); err == nil {
		return filepath.Dir(p), nil
	}
	for _, pattern := range []string{
		"/usr/lib/postgresql/*/bin",
		"/usr/pgsql-*/bin",
		"/opt/homebrew/opt/postgresql@*/bin",
		"/usr/local/opt/postgresql@*/bin",
	} {
		matches, _ := filepath.Glob(pattern)
		sort.Sort(byVersion(matches))
		for i := len(matches) - 1; i >= 0; i-- {
			if hasServerBinaries(matches[i]) {
				return matches[i], nil
			}
		}
	}
	return "", fmt.Errorf("PostgreSQL server binaries not found; install postgresql or set PGH_BINDIR")
}

func hasServerBinaries(dir string) bool {
	for _, bin := range []string{"initdb", "pg_ctl", "postgres"} {
		if _, err := os.Stat(filepath.Join(dir, bin)); err != nil {
			return false
		}
	}
	return true
}

// byVersion sorts paths like /usr/lib/postgresql/14/bin numerically by the
// version component so 14 beats 9.6.
type byVersion []string

func (v byVersion) Len() int      { return len(v) }
func (v byVersion) Swap(i, j int) { v[i], v[j] = v[j], v[i] }
func (v byVersion) Less(i, j int) bool {
	return versionNum(v[i]) < versionNum(v[j])
}

func versionNum(p string) float64 {
	for part := range strings.SplitSeq(p, string(filepath.Separator)) {
		s := strings.TrimPrefix(part, "postgresql@")
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n
		}
	}
	return 0
}

func (d *DB) binDir() (string, error) {
	if d.BinDir != "" {
		return d.BinDir, nil
	}
	dir, err := FindBinDir()
	if err != nil {
		return "", err
	}
	d.BinDir = dir
	return dir, nil
}

func (d *DB) pgCommand(name string, args ...string) (*exec.Cmd, error) {
	dir, err := d.binDir()
	if err != nil {
		return nil, err
	}
	return exec.Command(filepath.Join(dir, name), args...), nil
}

// Initialized reports whether the mounted image contains a data directory.
func (d *DB) Initialized() bool {
	_, err := os.Stat(filepath.Join(d.DataDir(), "PG_VERSION"))
	return err == nil
}

// InitDB creates the PostgreSQL data directory inside the mounted image.
func (d *DB) InitDB() error {
	cmd, err := d.pgCommand("initdb",
		"-D", d.DataDir(),
		"-A", "trust",
		"-E", "UTF8",
		"--no-locale",
		"-U", currentUser())
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("initdb failed: %v: %s", err, out)
	}
	return nil
}

// Start launches the server via pg_ctl. The server listens on a Unix socket
// in SockDir, plus 127.0.0.1:port when port is nonzero.
func (d *DB) Start(port int) error {
	listen := "''"
	pgPort := 5432
	if port != 0 {
		listen = "127.0.0.1"
		pgPort = port
	}
	opts := fmt.Sprintf("-c listen_addresses=%s -c port=%d -c unix_socket_directories='%s'",
		listen, pgPort, d.SockDir())
	cmd, err := d.pgCommand("pg_ctl",
		"-D", d.DataDir(),
		"-l", d.LogFile(),
		"-o", opts,
		"-w", "-t", "60",
		"start")
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if tail := logTail(d.LogFile(), 15); tail != "" {
			msg += "\n--- " + d.LogFile() + " ---\n" + tail
		}
		return fmt.Errorf("pg_ctl start failed: %v: %s", err, msg)
	}
	return nil
}

// Stop shuts the server down with a fast shutdown.
func (d *DB) Stop() error {
	cmd, err := d.pgCommand("pg_ctl", "-D", d.DataDir(), "-m", "fast", "-w", "stop")
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_ctl stop failed: %v: %s", err, out)
	}
	return nil
}

func logTail(path string, lines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// ConnInfo describes a running server, parsed from postmaster.pid.
type ConnInfo struct {
	PID        int
	Port       int
	SockDir    string
	ListenAddr string
}

// Running returns connection info if the server is up, or nil if it is not.
func (d *DB) Running() (*ConnInfo, error) {
	info, err := parsePostmasterPid(filepath.Join(d.DataDir(), "postmaster.pid"))
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTCONN) {
			return nil, nil
		}
		return nil, err
	}
	// A postmaster.pid can be left behind by a crash; check the process.
	if syscall.Kill(info.PID, 0) != nil {
		return nil, nil
	}
	return info, nil
}

// parsePostmasterPid reads the PG10+ postmaster.pid format:
// pid, datadir, start time, port, socket dir, listen addr, shmem, status.
func parsePostmasterPid(path string) (*ConnInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 6 {
		return nil, fmt.Errorf("%s: unexpected format", path)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return nil, fmt.Errorf("%s: bad pid: %v", path, err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[3]))
	if err != nil {
		return nil, fmt.Errorf("%s: bad port: %v", path, err)
	}
	return &ConnInfo{
		PID:        pid,
		Port:       port,
		SockDir:    strings.TrimSpace(lines[4]),
		ListenAddr: strings.TrimSpace(lines[5]),
	}, nil
}

// URL builds a connection string for the running server, preferring the Unix
// socket.
func (info *ConnInfo) URL() string {
	u := url.URL{
		Scheme: "postgresql",
		User:   url.User(currentUser()),
		Path:   "/postgres",
	}
	if info.SockDir != "" {
		q := url.Values{}
		q.Set("host", info.SockDir)
		q.Set("port", strconv.Itoa(info.Port))
		u.RawQuery = q.Encode()
	} else {
		host := info.ListenAddr
		if host == "" || host == "*" {
			host = "127.0.0.1"
		}
		u.Host = fmt.Sprintf("%s:%d", host, info.Port)
	}
	return u.String()
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
