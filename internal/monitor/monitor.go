// Package monitor collects live activity statistics from every running pgh
// database at once, for the `pgh top` command. It keeps one PostgreSQL
// connection per running server and samples pg_stat_activity and
// pg_stat_database on demand.
package monitor

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cwarden/pgh/internal/db"
	"github.com/jackc/pgx/v5"
)

// Backend is one row of pg_stat_activity from one server.
type Backend struct {
	Source      string  // database file's display name
	PID         int     // backend process id
	Database    string  // datname (empty for background workers)
	User        string  // usename
	State       string  // active, idle, idle in transaction, ...
	WaitType    string  // wait_event_type
	WaitEvent   string  // wait_event
	BackendType string  // client backend, autovacuum worker, ...
	QueryAge    float64 // seconds since query_start (0 if unknown)
	StateAge    float64 // seconds since state_change (0 if unknown)
	XactAge     float64 // seconds since xact_start (0 if unknown)
	Query       string  // current or most recent query text
}

// ServerStat summarizes one running database.
type ServerStat struct {
	Source   string
	Image    string
	Version  string
	Backends int
	Active   int
	Idle     int
	IdleTx   int
	TPS      float64 // transactions/sec since the previous sample
	CacheHit float64 // buffer cache hit ratio %, or -1 when unknown
	Err      error   // non-nil if this server could not be sampled
}

// Snapshot is one sampling of all running databases.
type Snapshot struct {
	Servers  []ServerStat
	Backends []Backend
	Taken    time.Time
}

// Monitor holds persistent connections to the running databases and samples
// them. It is safe for a single owner to call Refresh repeatedly; Refresh is
// not meant to be called concurrently with itself.
type Monitor struct {
	binDir string

	mu    sync.Mutex
	conns map[string]*serverConn // keyed by state dir
}

type serverConn struct {
	source   string
	image    string
	conn     *pgx.Conn
	version  string
	prev     counters
	prevAt   time.Time
	havePrev bool
}

// counters holds the cumulative pg_stat_database totals used for rate deltas.
type counters struct {
	xact float64 // commits + rollbacks
	hit  float64 // blocks served from cache
	read float64 // blocks read from disk
}

// New returns a Monitor. binDir is passed through to db.DB for binary
// discovery; it may be empty.
func New(binDir string) *Monitor {
	return &Monitor{binDir: binDir, conns: map[string]*serverConn{}}
}

// Close drops every connection.
func (m *Monitor) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, sc := range m.conns {
		sc.conn.Close(context.Background())
		delete(m.conns, key)
	}
}

// SourceName is the short display name for a database file: its base name
// without the extension.
func SourceName(image string) string {
	base := filepath.Base(image)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

type target struct {
	d    *db.DB
	info *db.ConnInfo
}

// runningTargets returns the databases that currently have a live server.
func (m *Monitor) runningTargets() map[string]target {
	targets := map[string]target{}
	dbs, err := db.All()
	if err != nil {
		return targets
	}
	for _, d := range dbs {
		d.BinDir = m.binDir
		if !d.ImageExists() {
			continue
		}
		info, err := d.Running()
		if err != nil || info == nil {
			continue
		}
		targets[d.StateDir] = target{d: d, info: info}
	}
	return targets
}

// Refresh samples every running database and returns a combined snapshot.
// Servers that started since the last call are connected; servers that
// stopped are dropped.
func (m *Monitor) Refresh(ctx context.Context) Snapshot {
	targets := m.runningTargets()

	// Drop connections to servers that are no longer running.
	m.mu.Lock()
	for key, sc := range m.conns {
		if _, ok := targets[key]; !ok {
			sc.conn.Close(context.Background())
			delete(m.conns, key)
		}
	}
	m.mu.Unlock()

	type result struct {
		stat     ServerStat
		backends []Backend
	}
	results := make([]result, len(targets))
	var wg sync.WaitGroup
	i := 0
	for key, t := range targets {
		idx := i
		i++
		wg.Add(1)
		go func(key string, t target) {
			defer wg.Done()
			stat, backends := m.sampleServer(ctx, key, t)
			results[idx] = result{stat: stat, backends: backends}
		}(key, t)
	}
	wg.Wait()

	snap := Snapshot{Taken: time.Now()}
	for _, r := range results {
		snap.Servers = append(snap.Servers, r.stat)
		snap.Backends = append(snap.Backends, r.backends...)
	}
	sort.Slice(snap.Servers, func(a, b int) bool {
		return snap.Servers[a].Source < snap.Servers[b].Source
	})
	return snap
}

func (m *Monitor) sampleServer(ctx context.Context, key string, t target) (ServerStat, []Backend) {
	stat := ServerStat{
		Source:   SourceName(t.d.Image),
		Image:    t.d.Image,
		CacheHit: -1,
	}
	sc, err := m.ensureConn(ctx, key, t)
	if err != nil {
		stat.Err = err
		return stat, nil
	}
	stat.Version = sc.version

	backends, err := m.queryBackends(ctx, sc)
	if err != nil {
		// The server may have restarted or shut down; drop the connection so
		// the next Refresh reconnects.
		m.drop(key)
		stat.Err = err
		return stat, nil
	}
	stat.Backends = len(backends)
	for _, b := range backends {
		switch {
		case b.State == "active":
			stat.Active++
		case b.State == "idle":
			stat.Idle++
		case strings.HasPrefix(b.State, "idle in transaction"):
			stat.IdleTx++
		}
	}

	m.sampleCounters(ctx, sc, &stat)
	return stat, backends
}

// ensureConn returns the cached connection for a server, dialing a new one on
// first use.
func (m *Monitor) ensureConn(ctx context.Context, key string, t target) (*serverConn, error) {
	m.mu.Lock()
	sc := m.conns[key]
	m.mu.Unlock()
	if sc != nil {
		return sc, nil
	}

	conn, err := pgx.Connect(ctx, t.info.URL())
	if err != nil {
		return nil, err
	}
	var version string
	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		version = ""
	}
	sc = &serverConn{
		source:  SourceName(t.d.Image),
		image:   t.d.Image,
		conn:    conn,
		version: version,
	}
	m.mu.Lock()
	m.conns[key] = sc
	m.mu.Unlock()
	return sc, nil
}

func (m *Monitor) drop(key string) {
	m.mu.Lock()
	if sc := m.conns[key]; sc != nil {
		sc.conn.Close(context.Background())
		delete(m.conns, key)
	}
	m.mu.Unlock()
}

const backendQuery = `
SELECT pid,
       COALESCE(datname, ''),
       COALESCE(usename, ''),
       COALESCE(state, ''),
       COALESCE(wait_event_type, ''),
       COALESCE(wait_event, ''),
       COALESCE(backend_type, ''),
       COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - query_start)), 0),
       COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - state_change)), 0),
       COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - xact_start)), 0),
       COALESCE(query, '')
FROM pg_stat_activity
WHERE pid <> pg_backend_pid()
  AND backend_type IN ('client backend', 'autovacuum worker')`

func (m *Monitor) queryBackends(ctx context.Context, sc *serverConn) ([]Backend, error) {
	rows, err := sc.conn.Query(ctx, backendQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backends []Backend
	for rows.Next() {
		b := Backend{Source: sc.source}
		if err := rows.Scan(&b.PID, &b.Database, &b.User, &b.State,
			&b.WaitType, &b.WaitEvent, &b.BackendType,
			&b.QueryAge, &b.StateAge, &b.XactAge, &b.Query); err != nil {
			return nil, err
		}
		b.Query = normalizeQuery(b.Query)
		backends = append(backends, b)
	}
	return backends, rows.Err()
}

const counterQuery = `
SELECT COALESCE(SUM(xact_commit + xact_rollback), 0),
       COALESCE(SUM(blks_hit), 0),
       COALESCE(SUM(blks_read), 0)
FROM pg_stat_database`

// sampleCounters reads cumulative counters and turns the delta since the
// previous sample into a TPS and cache-hit ratio.
func (m *Monitor) sampleCounters(ctx context.Context, sc *serverConn, stat *ServerStat) {
	var cur counters
	if err := sc.conn.QueryRow(ctx, counterQuery).Scan(&cur.xact, &cur.hit, &cur.read); err != nil {
		return
	}
	now := time.Now()
	if sc.havePrev {
		dt := now.Sub(sc.prevAt).Seconds()
		if dt > 0 {
			stat.TPS = (cur.xact - sc.prev.xact) / dt
			if stat.TPS < 0 {
				stat.TPS = 0 // counters reset (server restarted)
			}
		}
		dhit := cur.hit - sc.prev.hit
		dread := cur.read - sc.prev.read
		if dhit+dread > 0 {
			stat.CacheHit = 100 * dhit / (dhit + dread)
		}
	}
	sc.prev = cur
	sc.prevAt = now
	sc.havePrev = true
}

// normalizeQuery collapses internal whitespace so multi-line queries render
// on a single table row.
func normalizeQuery(q string) string {
	return strings.Join(strings.Fields(q), " ")
}
