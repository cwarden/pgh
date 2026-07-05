package cmd

import (
	"fmt"

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
		if len(dbs) == 0 {
			fmt.Println("no databases")
			return nil
		}
		for _, d := range dbs {
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
		fmt.Printf("%s: mounted at %s, server stopped\n", d.Image, d.MountDir())
		return nil
	}
	fmt.Printf("%s: running (pid %d)\n  %s\n", d.Image, info.PID, info.URL())
	return nil
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
