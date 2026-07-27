package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Connections summarizes the client backends attached to a running server.
type Connections struct {
	// Total is the number of client connections, excluding the one used to
	// take the count.
	Total int
	// Active is how many of those are doing something: anything whose state
	// is not plain "idle".
	Active int
}

const connectionsQuery = `
SELECT count(*),
       count(*) FILTER (WHERE COALESCE(state, '') <> 'idle')
FROM pg_stat_activity
WHERE backend_type = 'client backend'
  AND pid <> pg_backend_pid()`

// Connections counts the client connections to a running server.
func (info *ConnInfo) Connections(ctx context.Context) (Connections, error) {
	var c Connections
	conn, err := pgx.Connect(ctx, info.URL())
	if err != nil {
		return c, err
	}
	defer conn.Close(ctx)
	err = conn.QueryRow(ctx, connectionsQuery).Scan(&c.Total, &c.Active)
	return c, err
}
