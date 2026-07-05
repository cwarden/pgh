// Package cmd implements the pgh command line interface.
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"github.com/cwarden/pgh/internal/db"
	"github.com/spf13/cobra"
)

var (
	flagSize   string
	flagPort   int
	flagBinDir string
)

var rootCmd = &cobra.Command{
	Use:   "pgh [flags] DBFILE [psql-args...]",
	Short: "Single-file PostgreSQL databases, sqlite3-style",
	Long: `pgh manages PostgreSQL databases stored in single files. Each database
file is an ext4 filesystem image mounted without root via fuse2fs, containing
a PostgreSQL data directory.

Running "pgh mydb.pdb" creates the file if needed, mounts it, starts
PostgreSQL, and connects with psql. When psql exits, the server is stopped
and the image unmounted — unless the server was already running (e.g.
started with "pgh start"), in which case it is left alone.

Arguments after DBFILE are passed to psql:

  pgh mydb.pdb -c 'select 1'`,
	Args: cobra.MinimumNArgs(1),
	RunE: runShell,
	// Leave flags after DBFILE for psql.
	SilenceUsage: true,
}

// Execute runs the CLI and returns a process exit code.
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		return 1
	}
	return exitCode
}

// exitCode lets runShell propagate psql's exit status through Execute
// without skipping deferred cleanup.
var exitCode int

func init() {
	rootCmd.Flags().SetInterspersed(false)
	for _, cmd := range []*cobra.Command{rootCmd, startCmd} {
		cmd.Flags().StringVarP(&flagSize, "size", "s", db.FormatSize(db.DefaultImageSize),
			"size of a newly created database file (sparse), e.g. 512M, 2G")
		cmd.Flags().IntVarP(&flagPort, "port", "p", 0,
			"also listen on 127.0.0.1:PORT (default: Unix socket only)")
	}
	rootCmd.PersistentFlags().StringVar(&flagBinDir, "bindir", "",
		"PostgreSQL binary directory (default: autodetect)")
}

func openDB(image string) (*db.DB, error) {
	d, err := db.New(image)
	if err != nil {
		return nil, err
	}
	d.BinDir = flagBinDir
	return d, nil
}

func upOptions() (db.UpOptions, error) {
	size, err := db.ParseSize(flagSize)
	if err != nil {
		return db.UpOptions{}, err
	}
	return db.UpOptions{Size: size, Port: flagPort}, nil
}

func runShell(cmd *cobra.Command, args []string) error {
	d, err := openDB(args[0])
	if err != nil {
		return err
	}
	opts, err := upOptions()
	if err != nil {
		return err
	}
	info, started, err := d.Up(opts)
	if err != nil {
		return err
	}
	if started {
		defer func() {
			if err := d.Down(); err != nil {
				fmt.Fprintf(os.Stderr, "pgh: cleanup: %v\n", err)
			}
		}()
	}
	exitCode = runPsql(d, info, args[1:])
	return nil
}

// runPsql runs psql attached to the terminal and returns its exit code.
func runPsql(d *db.DB, info *db.ConnInfo, extraArgs []string) int {
	psql := "psql"
	if d.BinDir != "" {
		if _, err := os.Stat(filepath.Join(d.BinDir, "psql")); err == nil {
			psql = filepath.Join(d.BinDir, "psql")
		}
	} else if _, err := exec.LookPath("psql"); err != nil {
		if dir, derr := db.FindBinDir(); derr == nil {
			psql = filepath.Join(dir, "psql")
		}
	}
	args := append([]string{info.URL()}, extraArgs...)
	c := exec.Command(psql, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	// Ctrl-C is psql's business (query cancellation); don't let it kill pgh
	// before cleanup runs.
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "pgh: %v\n", err)
		return 1
	}
	return 0
}
