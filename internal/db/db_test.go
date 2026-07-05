package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateNameDistinctAndStable(t *testing.T) {
	a := stateName("/home/alice/temp.pdb")
	b := stateName("/home/bob/temp.pdb")
	if a == b {
		t.Errorf("different paths mapped to the same state name %q", a)
	}
	if a != stateName("/home/alice/temp.pdb") {
		t.Error("state name is not stable for the same path")
	}
	if !strings.HasPrefix(a, "temp-") {
		t.Errorf("state name %q should start with sanitized basename", a)
	}
}

func TestStateNameSanitizes(t *testing.T) {
	n := stateName("/tmp/my db (test).pdb")
	if strings.ContainsAny(n, " ()") {
		t.Errorf("state name %q contains unsafe characters", n)
	}
}

func TestNewUsesStateBase(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", base)
	d, err := New("some.pdb")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(d.Image) {
		t.Errorf("image path %q is not absolute", d.Image)
	}
	if filepath.Dir(d.StateDir) != base {
		t.Errorf("state dir %q not under %q", d.StateDir, base)
	}
}

func TestParsePostmasterPid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "postmaster.pid")
	content := "12345\n/data\n1751700000\n5432\n/run/user/1000/pgh/x/sock\n\n  1001 42\nready\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := parsePostmasterPid(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.PID != 12345 || info.Port != 5432 || info.SockDir != "/run/user/1000/pgh/x/sock" || info.ListenAddr != "" {
		t.Errorf("unexpected parse result: %+v", info)
	}
}

func TestConnInfoURL(t *testing.T) {
	unix := &ConnInfo{PID: 1, Port: 5432, SockDir: "/run/pgh/sock"}
	u := unix.URL()
	if !strings.Contains(u, "host=%2Frun%2Fpgh%2Fsock") || !strings.Contains(u, "port=5432") {
		t.Errorf("unix URL missing socket dir or port: %q", u)
	}
	if !strings.HasPrefix(u, "postgresql://") {
		t.Errorf("unexpected scheme in %q", u)
	}

	tcp := &ConnInfo{PID: 1, Port: 5555, SockDir: "", ListenAddr: "127.0.0.1"}
	u = tcp.URL()
	if !strings.Contains(u, "127.0.0.1:5555") {
		t.Errorf("tcp URL missing host:port: %q", u)
	}
}
