package cmd

import (
	"fmt"
	"os"

	"github.com/cwarden/pgh/internal/db"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status [DBFILE]",
	Short: "Show database status (all known databases when no file is given)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			d, err := openDB(args[0])
			if err != nil {
				return err
			}
			return printStatus(d)
		}
		dbs, err := db.All()
		if err != nil {
			return err
		}
		var kept []*db.DB
		for _, d := range dbs {
			if d.ImageExists() {
				kept = append(kept, d)
				continue
			}
			if err := d.Cleanup(); err != nil {
				fmt.Fprintf(os.Stderr, "pgh: cleaning up state for deleted %s: %v\n", d.Image, err)
			}
		}
		if len(kept) == 0 {
			fmt.Println("no databases")
			return nil
		}
		for _, d := range kept {
			if err := printStatus(d); err != nil {
				fmt.Printf("%s: %v\n", d.Image, err)
			}
		}
		return nil
	},
	SilenceUsage: true,
}

func printStatus(d *db.DB) error {
	if !d.ImageExists() {
		// Asked about explicitly, so report it — but still drop stale state.
		if err := d.Cleanup(); err != nil {
			return err
		}
		fmt.Printf("%s: no such file\n", d.Image)
		return nil
	}
	mounted, err := d.Mounted()
	if err != nil {
		return err
	}
	if !mounted {
		fmt.Printf("%s: stopped\n", d.Image)
		return nil
	}
	info, err := d.Running()
	if err != nil {
		return err
	}
	if info == nil {
		fmt.Printf("%s: open at %s (%s), server stopped\n", d.Image, d.MountDir(), d.MountBackend())
		return nil
	}
	fmt.Printf("%s: running (pid %d, %s)\n  %s\n", d.Image, info.PID, d.MountBackend(), info.URL())
	return nil
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
