package db

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// A server started with a bind address accepts TCP connections from other
// machines. Connections over the Unix socket and from loopback addresses
// keep trust authentication; connections from any other address must give
// the superuser's password (scram-sha-256), which pgh generates once and
// keeps in the image so it stays the same across restarts.

// ValidateBind checks a bind address: an IP address, or "*" for every
// interface.
func ValidateBind(bind string) error {
	if bind == "*" || net.ParseIP(bind) != nil {
		return nil
	}
	return fmt.Errorf("invalid bind address %q: give an IP address, or * for every interface", bind)
}

// isWildcard reports whether bind listens on every interface.
func isWildcard(bind string) bool {
	if bind == "*" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsUnspecified()
}

// listenAddresses returns PostgreSQL's listen_addresses for a bind address.
// The bind address comes first, because postmaster.pid records only the
// first one, and the network URL is built from it. A wildcard already
// covers the loopback interface; any other address is joined by 127.0.0.1,
// so local TCP clients keep working.
func listenAddresses(bind string) string {
	if isWildcard(bind) {
		return bind
	}
	if ip := net.ParseIP(bind); ip != nil && ip.Equal(net.IPv4(127, 0, 0, 1)) {
		return bind
	}
	return bind + ",127.0.0.1"
}

// hbaConfig is the client authentication file for a server with a bind
// address.
const hbaConfig = `# Written by pgh for a server started with --bind.
local all all trust
host all all 127.0.0.1/32 trust
host all all ::1/128 trust
host all all 0.0.0.0/0 scram-sha-256
host all all ::/0 scram-sha-256
`

// HBAFile is the client authentication file a server with a bind address
// uses. It lives in the state dir, so a server started without --bind uses
// the data directory's own pg_hba.conf.
func (d *DB) HBAFile() string { return filepath.Join(d.StateDir, "pg_hba.conf") }

// PasswordFile holds the superuser's password for network connections. It
// lives inside the image, so the password travels with the database.
func (d *DB) PasswordFile() string { return filepath.Join(d.MountDir(), "pgh-password") }

func (d *DB) writeHBAFile() error {
	return os.WriteFile(d.HBAFile(), []byte(hbaConfig), 0o600)
}

// password returns the superuser's network password, generating and storing
// it on first use.
func (d *DB) password() (string, error) {
	data, err := os.ReadFile(d.PasswordFile())
	if err == nil {
		if pw := strings.TrimSpace(string(data)); pw != "" {
			return pw, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	pw := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.WriteFile(d.PasswordFile(), []byte(pw+"\n"), 0o600); err != nil {
		return "", err
	}
	return pw, nil
}

// setPassword gives the superuser the network password, connecting over the
// Unix socket, where trust authentication applies.
func (d *DB) setPassword(info *ConnInfo) error {
	pw, err := d.password()
	if err != nil {
		return err
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, info.URL())
	if err != nil {
		return fmt.Errorf("connecting to set the network password: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "SET password_encryption = 'scram-sha-256'"); err != nil {
		return err
	}
	// ALTER ROLE takes no parameters; the password is base64url, which has
	// no quote characters.
	role := pgx.Identifier{currentUser()}.Sanitize()
	if _, err := conn.Exec(ctx, fmt.Sprintf("ALTER ROLE %s PASSWORD '%s'", role, pw)); err != nil {
		return fmt.Errorf("setting the network password: %v", err)
	}
	return nil
}

// NetworkURL returns the connection string other machines use to reach a
// server started with a bind address, with the superuser's password, and
// false when the server accepts no connections from other machines. For a
// server listening on every interface, the host is this machine's name.
func (d *DB) NetworkURL(info *ConnInfo) (string, bool, error) {
	host := info.ListenAddr
	if host == "" {
		return "", false, nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return "", false, nil
	}
	if isWildcard(host) {
		name, err := os.Hostname()
		if err != nil {
			return "", false, fmt.Errorf("reading the host name for the network URL: %v", err)
		}
		host = name
	}
	pw, err := d.password()
	if err != nil {
		return "", false, err
	}
	u := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(currentUser(), pw),
		Host:   net.JoinHostPort(host, strconv.Itoa(info.Port)),
		Path:   "/postgres",
	}
	return u.String(), true, nil
}

// checkBind reports an error when a running server does not listen on the
// bind address a caller asked for.
func checkBind(info *ConnInfo, bind string) error {
	if bind == "" {
		return nil
	}
	if info.ListenAddr == bind {
		return nil
	}
	listening := info.ListenAddr
	if listening == "" {
		listening = "the Unix socket only"
	}
	return fmt.Errorf("the server is already running and listens on %s, not %s; stop it and start it again with --bind", listening, bind)
}
