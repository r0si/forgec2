// Command dbq runs a SQL query against the ForgeC2 PostgreSQL database and
// prints the results.
//
// Usage: go run ./cmd/dbq <dsn> <query>
//
// Example:
//   go run ./cmd/dbq "host=localhost user=forgec2 password=forgec2 dbname=forgec2 port=5432 sslmode=disable" "SELECT * FROM users"
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/lib/pq"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: go run ./cmd/dbq <dsn> <query>")
		os.Exit(1)
	}
	db, err := sql.Open("postgres", os.Args[1])
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		fmt.Println("ping:", err)
		os.Exit(1)
	}
	q := os.Args[2]
	rows, err := db.Query(q)
	if err != nil {
		fmt.Println("query:", err)
		os.Exit(1)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	dest := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range dest {
		ptrs[i] = &dest[i]
	}
	n := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			fmt.Println("scan:", err)
			return
		}
		for i, c := range cols {
			fmt.Printf("%s=%v ", c, dest[i])
		}
		fmt.Println()
		n++
	}
	fmt.Println("rows:", n)
}
