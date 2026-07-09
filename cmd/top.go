package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cwarden/pgh/internal/monitor"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

var flagInterval time.Duration

var topCmd = &cobra.Command{
	Use:   "top",
	Short: "Live activity monitor across all running pgh databases",
	Long: `top shows a pg_top-style live view of every running pgh database at
once: a per-database summary (connections, transactions/sec, cache hit ratio)
and a combined list of backends from pg_stat_activity across all of them.

Only databases with a running server appear; start one with "pgh start" or an
interactive "pgh DBFILE" session first. The view refreshes on an interval and
picks up databases as they start and stop.

Keys: q quit · s cycle sort · r reverse · a active-only · +/- interval.

When stdout is not a terminal, top prints a single snapshot and exits.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		mon := monitor.New(flagBinDir)
		defer mon.Close()

		if !isatty.IsTerminal(os.Stdout.Fd()) {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			snap := mon.Refresh(ctx)
			fmt.Print(renderPlain(snap))
			return nil
		}

		p := tea.NewProgram(newTopModel(mon, flagInterval), tea.WithAltScreen())
		_, err := p.Run()
		return err
	},
}

func init() {
	topCmd.Flags().DurationVarP(&flagInterval, "interval", "i", 2*time.Second,
		"refresh interval")
	rootCmd.AddCommand(topCmd)
}

// sortField selects how backends are ordered.
type sortField int

const (
	sortTime sortField = iota // longest-running first
	sortSource
	sortDatabase
	sortState
	sortPID
)

func (s sortField) String() string {
	switch s {
	case sortTime:
		return "time"
	case sortSource:
		return "source"
	case sortDatabase:
		return "database"
	case sortState:
		return "state"
	case sortPID:
		return "pid"
	}
	return "?"
}

func (s sortField) next() sortField { return (s + 1) % 5 }

// statePriority ranks states so busy backends sort ahead of idle ones.
func statePriority(state string) int {
	switch {
	case state == "active":
		return 0
	case strings.HasPrefix(state, "idle in transaction"):
		return 1
	case state == "idle":
		return 3
	default:
		return 2
	}
}

// sortBackends orders backends in place by the given field. Ties break by
// source then pid so the order is stable across refreshes. reverse flips the
// primary comparison.
func sortBackends(backends []monitor.Backend, field sortField, reverse bool) {
	less := func(a, b monitor.Backend) bool {
		switch field {
		case sortSource:
			if a.Source != b.Source {
				return a.Source < b.Source
			}
		case sortDatabase:
			if a.Database != b.Database {
				return a.Database < b.Database
			}
		case sortState:
			if pa, pb := statePriority(a.State), statePriority(b.State); pa != pb {
				return pa < pb
			}
		case sortPID:
			if a.PID != b.PID {
				return a.PID < b.PID
			}
		case sortTime:
			if a.QueryAge != b.QueryAge {
				return a.QueryAge > b.QueryAge // longest first
			}
		}
		return false
	}
	sort.SliceStable(backends, func(i, j int) bool {
		a, b := backends[i], backends[j]
		if less(a, b) {
			return !reverse
		}
		if less(b, a) {
			return reverse
		}
		// Stable tiebreak, unaffected by reverse.
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.PID < b.PID
	})
}

// filterBackends drops idle backends unless showAll is set.
func filterBackends(backends []monitor.Backend, showAll bool) []monitor.Backend {
	if showAll {
		return backends
	}
	kept := make([]monitor.Backend, 0, len(backends))
	for _, b := range backends {
		if b.State != "idle" && b.State != "" {
			kept = append(kept, b)
		}
	}
	return kept
}

// formatAge renders a duration in seconds compactly: "0.4s", "12s", "3m04s",
// "1h02m". A non-positive age (unknown) renders as a dash.
func formatAge(seconds float64) string {
	if seconds <= 0 {
		return "-"
	}
	if seconds < 10 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	s := int(seconds + 0.5)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
}

// formatWait joins the wait event type and event, or a dash when idle/none.
func formatWait(b monitor.Backend) string {
	switch {
	case b.WaitType == "" && b.WaitEvent == "":
		return "-"
	case b.WaitEvent == "":
		return b.WaitType
	default:
		return b.WaitType + ":" + b.WaitEvent
	}
}

// shortVersion keeps only the numeric version ("14.23 (Ubuntu ...)" -> "14.23").
func shortVersion(v string) string {
	if i := strings.IndexByte(v, ' '); i >= 0 {
		return v[:i]
	}
	return v
}
