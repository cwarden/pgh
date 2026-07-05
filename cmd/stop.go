package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop DBFILE",
	Short: "Stop the database and unmount its file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		d, err := openDB(args[0])
		if err != nil {
			return err
		}
		if err := d.Down(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "stopped %s\n", d.Image)
		return nil
	},
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
