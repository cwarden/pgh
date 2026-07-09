package cmd

import (
	"fmt"
	"strings"

	"github.com/cwarden/pgh/internal/monitor"
)

// renderPlain formats a snapshot as unstyled text for non-interactive output
// (pipes, files, tests). It mirrors the TUI's two sections without ANSI.
func renderPlain(snap monitor.Snapshot) string {
	var b strings.Builder

	total, active := 0, 0
	for _, s := range snap.Servers {
		total += s.Backends
		active += s.Active
	}
	fmt.Fprintf(&b, "pgh top — %d database%s, %d backend%s, %d active\n\n",
		len(snap.Servers), plural(len(snap.Servers)),
		total, plural(total), active)

	if len(snap.Servers) == 0 {
		b.WriteString("no running databases\n")
		return b.String()
	}

	fmt.Fprintf(&b, "%-14s %-8s %6s %6s %8s %7s\n",
		"SOURCE", "PG", "CONNS", "ACTIVE", "TPS", "CACHE%")
	for _, s := range snap.Servers {
		if s.Err != nil {
			fmt.Fprintf(&b, "%-14s error: %v\n", truncate(s.Source, 14), s.Err)
			continue
		}
		cache := "-"
		if s.CacheHit >= 0 {
			cache = fmt.Sprintf("%.1f", s.CacheHit)
		}
		fmt.Fprintf(&b, "%-14s %-8s %6d %6d %8.1f %7s\n",
			truncate(s.Source, 14), shortVersion(s.Version),
			s.Backends, s.Active, s.TPS, cache)
	}

	backends := filterBackends(snap.Backends, true)
	sortBackends(backends, sortTime, false)
	if len(backends) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "\n%-14s %7s %-12s %-10s %-22s %-18s %8s %s\n",
		"SOURCE", "PID", "DATABASE", "USER", "STATE", "WAIT", "TIME", "QUERY")
	for _, bk := range backends {
		fmt.Fprintf(&b, "%-14s %7d %-12s %-10s %-22s %-18s %8s %s\n",
			truncate(bk.Source, 14), bk.PID,
			truncate(bk.Database, 12), truncate(bk.User, 10),
			truncate(bk.State, 22), truncate(formatWait(bk), 18),
			formatAge(bk.QueryAge), truncate(bk.Query, 60))
	}
	return b.String()
}
