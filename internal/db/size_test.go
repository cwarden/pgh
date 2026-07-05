package db

import "testing"

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1G", 1 << 30},
		{"1g", 1 << 30},
		{"1GB", 1 << 30},
		{"512M", 512 << 20},
		{"512MB", 512 << 20},
		{"64K", 64 << 10},
		{"2T", 2 << 40},
		{"1.5G", 3 << 29},
		{"1073741824", 1 << 30},
		{" 2G ", 2 << 30},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if err != nil {
			t.Errorf("ParseSize(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseSizeInvalid(t *testing.T) {
	for _, in := range []string{"", "abc", "-1G", "0", "G", "1X"} {
		if n, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, want error", in, n)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{1 << 30, "1G"},
		{512 << 20, "512M"},
		{2 << 40, "2T"},
		{64 << 10, "64K"},
		{1000, "1000"},
	}
	for _, c := range cases {
		if got := FormatSize(c.in); got != c.want {
			t.Errorf("FormatSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
