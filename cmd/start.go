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
Use "pgh stop DBFILE" to shut it down and unmount.

With --bind and --port, the server also listens on that address for
connections from other machines, and start prints the connection string
they use instead: the bind address (or, for *, this machine's host name), the
port, and a password pgh generated and keeps in the database file. Local
connections need no password; connections from other machines must give it.

  DB_URL=$(pgh start --bind '*' -p 5433 mydb.pdb)`,
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
		if err := resizeIfRequested(cmd, d); err != nil {
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
		ensureWatcher(d)
		networkURL, ok, err := d.NetworkURL(info)
		if err != nil {
			return err
		}
		if ok {
			fmt.Println(networkURL)
		} else {
			fmt.Println(info.URL())
		}
		return nil
	},
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(startCmd)
}
