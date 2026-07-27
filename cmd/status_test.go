package cmd

import (
	"testing"

	"github.com/cwarden/pgh/internal/db"
)

func TestFormatConnections(t *testing.T) {
	cases := []struct {
		in   db.Connections
		want string
	}{
		{db.Connections{}, "0 connections"},
		{db.Connections{Total: 1}, "1 connection (0 active)"},
		{db.Connections{Total: 1, Active: 1}, "1 connection (1 active)"},
		{db.Connections{Total: 4, Active: 2}, "4 connections (2 active)"},
	}
	for _, c := range cases {
		if got := formatConnections(c.in); got != c.want {
			t.Errorf("formatConnections(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("boom\ndetail\n"); got != "boom" {
		t.Errorf("firstLine = %q, want %q", got, "boom")
	}
	if got := firstLine("boom"); got != "boom" {
		t.Errorf("firstLine = %q, want %q", got, "boom")
	}
}
