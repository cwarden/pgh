package db

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestValidateBind(t *testing.T) {
	for _, ok := range []string{"*", "0.0.0.0", "10.1.2.3", "::", "fe80::1"} {
		if err := ValidateBind(ok); err != nil {
			t.Errorf("ValidateBind(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"localhost", "db.example.com", "10.1.2", ""} {
		if err := ValidateBind(bad); err == nil {
			t.Errorf("ValidateBind(%q) = nil, want an error", bad)
		}
	}
}

func TestListenAddressesPutsTheBindAddressFirst(t *testing.T) {
	cases := map[string]string{
		"*":         "*",
		"0.0.0.0":   "0.0.0.0",
		"::":        "::",
		"10.1.2.3":  "10.1.2.3,127.0.0.1",
		"127.0.0.1": "127.0.0.1",
	}
	for bind, want := range cases {
		if got := listenAddresses(bind); got != want {
			t.Errorf("listenAddresses(%q) = %q, want %q", bind, got, want)
		}
	}
}

func testDBWithMountDir(t *testing.T) *DB {
	t.Helper()
	t.Setenv("PGH_STATE_DIR", t.TempDir())
	d, err := New(filepath.Join(t.TempDir(), "net.pdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d.MountDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNetworkURLUsesTheBindAddressAndAStablePassword(t *testing.T) {
	d := testDBWithMountDir(t)

	if _, ok, err := d.NetworkURL(&ConnInfo{Port: 5433, SockDir: "/s"}); err != nil || ok {
		t.Errorf("a socket-only server has no network URL: ok=%v err=%v", ok, err)
	}
	if _, ok, err := d.NetworkURL(&ConnInfo{Port: 5433, ListenAddr: "127.0.0.1"}); err != nil || ok {
		t.Errorf("a loopback server has no network URL: ok=%v err=%v", ok, err)
	}

	raw, ok, err := d.NetworkURL(&ConnInfo{Port: 5433, ListenAddr: "10.1.2.3"})
	if err != nil || !ok {
		t.Fatalf("NetworkURL: ok=%v err=%v", ok, err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "10.1.2.3:5433" || u.Path != "/postgres" {
		t.Errorf("network URL %q: want host 10.1.2.3:5433 and path /postgres", raw)
	}
	pw, set := u.User.Password()
	if !set || len(pw) < 32 {
		t.Errorf("network URL %q carries no generated password", raw)
	}
	again, _, _ := d.NetworkURL(&ConnInfo{Port: 5433, ListenAddr: "10.1.2.3"})
	if again != raw {
		t.Errorf("the password changed between calls: %q then %q", raw, again)
	}
	if info, err := os.Stat(d.PasswordFile()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("password file: %v, mode %v; want mode 0600", err, info.Mode().Perm())
	}

	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = d.NetworkURL(&ConnInfo{Port: 5433, ListenAddr: "*"})
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(raw); u.Hostname() != hostname {
		t.Errorf("a wildcard server's URL %q should name host %q", raw, hostname)
	}

	raw, _, _ = d.NetworkURL(&ConnInfo{Port: 5433, ListenAddr: "fe80::1"})
	if u, _ := url.Parse(raw); u.Host != "[fe80::1]:5433" {
		t.Errorf("an IPv6 URL %q needs a bracketed host", raw)
	}
}

func TestCheckBindRejectsARunningServerWithoutTheAddress(t *testing.T) {
	if err := checkBind(&ConnInfo{ListenAddr: "10.1.2.3"}, ""); err != nil {
		t.Errorf("no bind asked: %v", err)
	}
	if err := checkBind(&ConnInfo{ListenAddr: "10.1.2.3"}, "10.1.2.3"); err != nil {
		t.Errorf("same bind: %v", err)
	}
	err := checkBind(&ConnInfo{}, "10.1.2.3")
	if err == nil || !strings.Contains(err.Error(), "Unix socket only") {
		t.Errorf("socket-only server: %v", err)
	}
}

// TestBindRequiresAPasswordFromOtherAddresses starts a server bound to
// 127.0.0.2. Linux delivers it over the loopback interface, but the client
// authentication file trusts only 127.0.0.1 and ::1, so it is treated as
// another machine.
func TestBindRequiresAPasswordFromOtherAddresses(t *testing.T) {
	skipWithoutLifecycleTools(t)
	base := t.TempDir()
	t.Setenv("PGH_STATE_DIR", filepath.Join(base, "state"))
	d, err := New(filepath.Join(base, "bind.pdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Down() })

	port := freePort(t)
	opts := UpOptions{Size: 300 << 20, Port: port, Bind: "127.0.0.2"}
	info, _, err := d.Up(opts)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if info.ListenAddr != "127.0.0.2" {
		t.Errorf("listen address %q, want 127.0.0.2 first", info.ListenAddr)
	}
	// NetworkURL gives no URL for a loopback address, which other machines
	// cannot reach, so the test builds the one it would give.
	addressURL := func() string {
		pw, err := d.password()
		if err != nil {
			t.Fatal(err)
		}
		u := url.URL{Scheme: "postgresql", User: url.UserPassword(currentUser(), pw), Host: "127.0.0.2:" + strconv.Itoa(port), Path: "/postgres"}
		return u.String()
	}
	networkURL := addressURL()

	ctx := context.Background()
	// Linux gives a connection to 127.0.0.2 the source address 127.0.0.1,
	// which the server trusts, so the test dials from 127.0.0.2.
	connect := func(connString string) error {
		config, err := pgx.ParseConfig(connString)
		if err != nil {
			return err
		}
		if config.Host == "127.0.0.2" {
			dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2")}}
			config.DialFunc = dialer.DialContext
		}
		conn, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return err
		}
		return conn.Close(ctx)
	}
	if err := connect(networkURL); err != nil {
		t.Errorf("connecting with the password: %v", err)
	}
	withoutPassword, _ := url.Parse(networkURL)
	withoutPassword.User = url.User(withoutPassword.User.Username())
	if err := connect(withoutPassword.String()); err == nil {
		t.Error("a connection from another address without the password succeeded")
	}
	if err := connect(info.URL()); err != nil {
		t.Errorf("the Unix socket needs no password: %v", err)
	}
	if err := connect("postgresql://" + currentUser() + "@127.0.0.1:" + strconv.Itoa(port) + "/postgres"); err != nil {
		t.Errorf("127.0.0.1 needs no password: %v", err)
	}

	if _, _, err := d.Up(UpOptions{Port: port, Bind: "127.0.0.3"}); err == nil {
		t.Error("Up with another bind address should fail while the server runs")
	}

	// The password stays the same across a restart.
	if err := d.Down(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = d.Up(opts); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	again := addressURL()
	if again != networkURL {
		t.Errorf("the network URL changed across a restart: %q then %q", networkURL, again)
	}
	if err := connect(again); err != nil {
		t.Errorf("connecting with the password after a restart: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	return lis.Addr().(*net.TCPAddr).Port
}
