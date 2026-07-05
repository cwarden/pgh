package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start DBFILE",
	Short: "Start the database in the background and print its connection string",
	Long: `Start mounts the database file (creating it first if it does not exist)
and starts PostgreSQL, leaving both running in the background. The
connection string is printed on stdout so other processes can connect:

  DB_URL=$(pgh start mydb.pdb)
  psql "$DB_URL"

If the server is already running, start just prints the connection string.
Use "pgh stop DBFILE" to shut it down and unmount.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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
			fmt.Fprintf(os.Stderr, "started %s\n", d.Image)
		} else {
			fmt.Fprintf(os.Stderr, "already running: %s\n", d.Image)
		}
		fmt.Println(info.URL())
		return nil
	},
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(startCmd)
}
