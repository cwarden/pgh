package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/cwarden/pgh/internal/db"
	"github.com/spf13/cobra"
)

// watchCmd is the internal entry point for the autogrow watcher process. It
// is spawned automatically for kernel-mounted databases and terminated by
// pgh stop; it exits on its own when the database closes.
var watchCmd = &cobra.Command{
	Use:    "__watch DBFILE",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		d, err := openDB(args[0])
		if err != nil {
			return err
		}
		if err := d.RecordWatcherPID(); err != nil {
			return err
		}
		d.Watch(250*time.Millisecond, func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, time.Now().Format(time.RFC3339)+" "+format+"\n", a...)
		})
		return nil
	},
	SilenceUsage: true,
}

// ensureWatcher spawns the autogrow watcher for a kernel-mounted database
// if one is not already running. Best effort: autogrow is a convenience,
// not a requirement.
func ensureWatcher(d *db.DB) {
	if d.MountBackend() != "kernel mount" || d.WatcherAlive() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	logFile, err := os.OpenFile(d.WatchLogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer logFile.Close()
	c := exec.Command(exe, "__watch", d.Image)
	if flagBinDir != "" {
		c.Args = append(c.Args, "--bindir", flagBinDir)
	}
	c.Stdout = logFile
	c.Stderr = logFile
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "pgh: starting autogrow watcher: %v\n", err)
		return
	}
	// Reap the child if we outlive it (foreground sessions); when we exit
	// first, init inherits it.
	go c.Wait()
}

func init() {
	rootCmd.AddCommand(watchCmd)
}
