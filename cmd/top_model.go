package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cwarden/pgh/internal/monitor"
)

// backend table column indices.
const (
	colSource = iota
	colPID
	colDatabase
	colUser
	colState
	colWait
	colTime
	colQuery
)

// fixed widths for every column except QUERY, which takes the remainder.
var fixedWidths = map[int]int{
	colSource:   14,
	colPID:      7,
	colDatabase: 12,
	colUser:     10,
	colState:    22,
	colWait:     18,
	colTime:     8,
}

var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	activeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	summaryTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Bold(true)
)

type snapshotMsg struct{ snap monitor.Snapshot }
type tickMsg struct{}

type topModel struct {
	mon      *monitor.Monitor
	snap     monitor.Snapshot
	table    table.Model
	interval time.Duration
	sort     sortField
	reverse  bool
	showAll  bool
	width    int
	height   int
	ready    bool
}

func newTopModel(mon *monitor.Monitor, interval time.Duration) topModel {
	t := table.New(table.WithFocused(true))
	s := table.DefaultStyles()
	s.Header = s.Header.BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("238")).BorderBottom(true).Bold(true)
	s.Selected = s.Selected.Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("238")).Bold(false)
	t.SetStyles(s)
	return topModel{
		mon:      mon,
		table:    t,
		interval: interval,
		sort:     sortTime,
		showAll:  true,
	}
}

func (m topModel) Init() tea.Cmd {
	return refreshCmd(m.mon)
}

// refreshCmd samples all databases off the UI goroutine.
func refreshCmd(mon *monitor.Monitor) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return snapshotMsg{snap: mon.Refresh(ctx)}
	}
}

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.layout()
		return m, nil

	case snapshotMsg:
		m.snap = msg.snap
		m.refreshRows()
		return m, tickCmd(m.interval)

	case tickMsg:
		return m, refreshCmd(m.mon)

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "s":
			m.sort = m.sort.next()
			m.refreshRows()
			return m, nil
		case "r":
			m.reverse = !m.reverse
			m.refreshRows()
			return m, nil
		case "a":
			m.showAll = !m.showAll
			m.refreshRows()
			return m, nil
		case "+", "=":
			if m.interval < 30*time.Second {
				m.interval += time.Second
			}
			return m, nil
		case "-", "_":
			if m.interval > time.Second {
				m.interval -= time.Second
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// layout recomputes column widths and table height for the current window.
func (m *topModel) layout() {
	if !m.ready {
		return
	}
	queryW := m.width - 2 // account for table cell padding
	for _, w := range fixedWidths {
		queryW -= w + 2
	}
	if queryW < 20 {
		queryW = 20
	}
	cols := []table.Column{
		{Title: "SOURCE", Width: fixedWidths[colSource]},
		{Title: "PID", Width: fixedWidths[colPID]},
		{Title: "DATABASE", Width: fixedWidths[colDatabase]},
		{Title: "USER", Width: fixedWidths[colUser]},
		{Title: "STATE", Width: fixedWidths[colState]},
		{Title: "WAIT", Width: fixedWidths[colWait]},
		{Title: "TIME", Width: fixedWidths[colTime]},
		{Title: "QUERY", Width: queryW},
	}
	m.table.SetColumns(cols)
	m.table.SetWidth(m.width)

	// Reserve rows for the summary block and footer; the rest is the table.
	summaryLines := len(m.snap.Servers) + 4
	footerLines := 2
	h := max(m.height-summaryLines-footerLines, 3)
	m.table.SetHeight(h)
}

// refreshRows re-sorts, filters, and re-lays out after new data or an option
// change.
func (m *topModel) refreshRows() {
	m.layout()
	backends := filterBackends(m.snap.Backends, m.showAll)
	sortBackends(backends, m.sort, m.reverse)

	queryW := fixedWidths[colState]
	if len(m.table.Columns()) > colQuery {
		queryW = m.table.Columns()[colQuery].Width
	}
	rows := make([]table.Row, 0, len(backends))
	for _, b := range backends {
		rows = append(rows, table.Row{
			b.Source,
			fmt.Sprintf("%d", b.PID),
			b.Database,
			b.User,
			b.State,
			formatWait(b),
			formatAge(b.QueryAge),
			truncate(b.Query, queryW),
		})
	}
	m.table.SetRows(rows)
	if m.table.Cursor() >= len(rows) {
		m.table.SetCursor(0)
	}
}

func (m topModel) View() string {
	if !m.ready {
		return "loading..."
	}
	var b strings.Builder

	total, active := 0, 0
	for _, s := range m.snap.Servers {
		total += s.Backends
		active += s.Active
	}
	title := titleStyle.Render("pgh top")
	stamp := m.snap.Taken.Format("15:04:05")
	summary := fmt.Sprintf("%d database%s · %d backend%s · %s active · %s",
		len(m.snap.Servers), plural(len(m.snap.Servers)),
		total, plural(total), activeStyle.Render(fmt.Sprintf("%d", active)), stamp)
	b.WriteString(title + "   " + dimStyle.Render(summary) + "\n")
	b.WriteString(m.serverTable() + "\n")

	if len(m.snap.Servers) == 0 {
		b.WriteString(dimStyle.Render("no running databases — start one with `pgh start FILE`") + "\n")
	} else {
		b.WriteString(m.table.View() + "\n")
	}

	b.WriteString(m.footer())
	return b.String()
}

// serverTable renders the per-database summary block.
func (m topModel) serverTable() string {
	var b strings.Builder
	b.WriteString(summaryTitle.Render(fmt.Sprintf(
		"%-14s %-8s %6s %6s %8s %7s", "SOURCE", "PG", "CONNS", "ACTIVE", "TPS", "CACHE%")) + "\n")
	for _, s := range m.snap.Servers {
		if s.Err != nil {
			fmt.Fprintf(&b, "%-14s %s\n",
				truncate(s.Source, 14), errStyle.Render("error: "+s.Err.Error()))
			continue
		}
		cache := "-"
		if s.CacheHit >= 0 {
			cache = fmt.Sprintf("%.1f", s.CacheHit)
		}
		active := fmt.Sprintf("%6d", s.Active)
		if s.Active > 0 {
			active = activeStyle.Render(active)
		}
		fmt.Fprintf(&b, "%-14s %-8s %6d %s %8.1f %7s\n",
			truncate(s.Source, 14), shortVersion(s.Version),
			s.Backends, active, s.TPS, cache)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m topModel) footer() string {
	sortName := m.sort.String()
	if m.reverse {
		sortName += " (rev)"
	}
	filter := "all"
	if !m.showAll {
		filter = "active"
	}
	help := fmt.Sprintf("q quit · s sort:%s · r reverse · a filter:%s · +/- interval:%s",
		sortName, filter, m.interval)
	return dimStyle.Render(help)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// truncate shortens s to fit width, adding an ellipsis when cut.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}
