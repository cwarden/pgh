package cmd

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cwarden/pgh/internal/monitor"
)

func TestFormatAge(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "-"},
		{-1, "-"},
		{0.42, "0.4s"},
		{9.9, "9.9s"},
		{12, "12s"},
		{59, "59s"},
		{60, "1m00s"},
		{184, "3m04s"},
		{3600, "1h00m"},
		{3720, "1h02m"},
	}
	for _, c := range cases {
		if got := formatAge(c.in); got != c.want {
			t.Errorf("formatAge(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatWait(t *testing.T) {
	cases := []struct {
		b    monitor.Backend
		want string
	}{
		{monitor.Backend{}, "-"},
		{monitor.Backend{WaitType: "Lock", WaitEvent: "relation"}, "Lock:relation"},
		{monitor.Backend{WaitType: "Client"}, "Client"},
	}
	for _, c := range cases {
		if got := formatWait(c.b); got != c.want {
			t.Errorf("formatWait(%+v) = %q, want %q", c.b, got, c.want)
		}
	}
}

func TestShortVersion(t *testing.T) {
	if got := shortVersion("14.23 (Ubuntu 14.23-1)"); got != "14.23" {
		t.Errorf("shortVersion = %q, want 14.23", got)
	}
	if got := shortVersion("16.2"); got != "16.2" {
		t.Errorf("shortVersion = %q, want 16.2", got)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		s     string
		width int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"hello", 1, "…"},
		{"hello", 0, ""},
	}
	for _, c := range cases {
		if got := truncate(c.s, c.width); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.s, c.width, got, c.want)
		}
	}
}

func TestModelRendersSnapshot(t *testing.T) {
	m := newTopModel(nil, 2*time.Second)
	// Size the window, then feed a snapshot, as bubbletea would.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 24})
	m = updated.(topModel)
	snap := monitor.Snapshot{
		Servers: []monitor.ServerStat{
			{Source: "alpha", Version: "14.23", Backends: 2, Active: 1, TPS: 12.5, CacheHit: 99.5},
			{Source: "beta", Version: "16.2", Backends: 1, CacheHit: -1},
		},
		Backends: []monitor.Backend{
			{Source: "alpha", PID: 4242, Database: "postgres", User: "cwarden",
				State: "active", WaitType: "Timeout", WaitEvent: "PgSleep",
				QueryAge: 1.0, Query: "select pg_sleep(30)"},
		},
	}
	updated, _ = m.Update(snapshotMsg{snap: snap})
	m = updated.(topModel)

	view := m.View()
	for _, want := range []string{"pgh top", "2 databases", "alpha", "beta", "14.23", "4242", "select pg_sleep(30)", "q quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\n---\n%s", want, view)
		}
	}
}

func TestModelKeysMutateState(t *testing.T) {
	m := newTopModel(nil, 2*time.Second)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(topModel)

	if m.sort != sortTime {
		t.Fatalf("initial sort = %v", m.sort)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = updated.(topModel)
	if m.sort != sortSource {
		t.Errorf("after s, sort = %v, want source", m.sort)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(topModel)
	if m.showAll {
		t.Error("after a, showAll should be false")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	m = updated.(topModel)
	if m.interval != 3*time.Second {
		t.Errorf("after +, interval = %v, want 3s", m.interval)
	}
}

func TestFilterBackends(t *testing.T) {
	backends := []monitor.Backend{
		{PID: 1, State: "active"},
		{PID: 2, State: "idle"},
		{PID: 3, State: "idle in transaction"},
		{PID: 4, State: ""},
	}
	if got := filterBackends(backends, true); len(got) != 4 {
		t.Errorf("showAll kept %d, want 4", len(got))
	}
	got := filterBackends(backends, false)
	if len(got) != 2 {
		t.Fatalf("active-only kept %d, want 2", len(got))
	}
	for _, b := range got {
		if b.State == "idle" || b.State == "" {
			t.Errorf("active-only kept idle pid %d", b.PID)
		}
	}
}

func TestSortBackendsByTime(t *testing.T) {
	backends := []monitor.Backend{
		{PID: 1, QueryAge: 1},
		{PID: 2, QueryAge: 5},
		{PID: 3, QueryAge: 3},
	}
	sortBackends(backends, sortTime, false)
	want := []int{2, 3, 1} // longest-running first
	for i, pid := range want {
		if backends[i].PID != pid {
			t.Errorf("sortTime position %d = pid %d, want %d", i, backends[i].PID, pid)
		}
	}
	sortBackends(backends, sortTime, true)
	if backends[0].PID != 1 {
		t.Errorf("reversed sortTime first = pid %d, want 1", backends[0].PID)
	}
}

func TestSortBackendsStableTiebreak(t *testing.T) {
	// Equal sort keys must fall back to source then pid deterministically.
	backends := []monitor.Backend{
		{Source: "b", PID: 20, State: "idle"},
		{Source: "a", PID: 10, State: "idle"},
		{Source: "a", PID: 5, State: "idle"},
	}
	sortBackends(backends, sortState, false)
	got := []string{}
	for _, b := range backends {
		got = append(got, b.Source+"/"+strconv.Itoa(b.PID))
	}
	want := "a/5,a/10,b/20"
	if strings.Join(got, ",") != want {
		t.Errorf("tiebreak order = %v, want %s", got, want)
	}
}
