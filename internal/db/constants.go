package db

import "time"

const (
	// PostgresPoolMaxOpenConns is the maximum number of open connections in the
	// PostgreSQL pool. This deployment uses a single writer pool (1) to avoid
	// connection contention under load. Operators can increase this if they
	// run pgBouncer or have a multi-writer setup.
	PostgresPoolMaxOpenConns = 1
	PostgresPoolMaxIdleConns = 1
	PostgresConnMaxLifetime  = 5 * time.Minute
	PostgresConnMaxIdleTime  = 2 * time.Minute
)
