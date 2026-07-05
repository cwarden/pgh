package db

import (
	"fmt"
	"strconv"
	"strings"
)

// MinImageSize is the smallest allowed image: initdb needs ~40MB and the WAL
// needs room to breathe.
const MinImageSize = 64 << 20

// DefaultImageSize is used when no --size is given. Images are sparse, so
// this only reserves address space, not disk.
const DefaultImageSize = 1 << 30

// ParseSize parses a human-friendly size like "1G", "512MB", or "1073741824"
// (bytes). Units are binary (K=1024).
func ParseSize(s string) (int64, error) {
	orig := s
	s = strings.TrimSpace(strings.ToUpper(s))
	s = strings.TrimSuffix(s, "B")
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult = 1 << 10
	case strings.HasSuffix(s, "M"):
		mult = 1 << 20
	case strings.HasSuffix(s, "G"):
		mult = 1 << 30
	case strings.HasSuffix(s, "T"):
		mult = 1 << 40
	}
	if mult > 1 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q", orig)
	}
	return int64(n * float64(mult)), nil
}

// FormatSize renders a byte count with a binary unit suffix.
func FormatSize(n int64) string {
	switch {
	case n >= 1<<40 && n%(1<<40) == 0:
		return fmt.Sprintf("%dT", n>>40)
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%dG", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%dM", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%dK", n>>10)
	default:
		return strconv.FormatInt(n, 10)
	}
}
