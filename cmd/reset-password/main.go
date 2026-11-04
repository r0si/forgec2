// Command reset-password updates a user's password in the ForgeC2 database.
// Usage: go run ./cmd/reset-password -dsn "host=..." -user labtest -pass newpassword
// Prefer -pass-stdin (or FORGEC2_RESET_PASS env) over -pass: argv is visible
// in process listings and shell history.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// default policy mirrors config.example.yaml password_policy (min 8 + upper/lower/digit).
func checkPasswordPolicy(password string) error {
	const minLen = 8
	if len(password) < minLen {
		return fmt.Errorf("password must be at least %d characters", minLen)
	}
	var hasUpper, hasLower, hasDigit bool
	for _, ch := range password {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasDigit = true
		}
	}
	if !hasUpper {
		return fmt.Errorf("password must contain at least one uppercase letter")
	}
	if !hasLower {
		return fmt.Errorf("password must contain at least one lowercase letter")
	}
	if !hasDigit {
		return fmt.Errorf("password must contain at least one digit")
	}
	return nil
}

func main() {
	dsn := flag.String("dsn", "host=localhost user=forgec2 password=forgec2 dbname=forgec2 port=5432 sslmode=disable", "PostgreSQL connection string")
	username := flag.String("user", "", "username to reset password for (required)")
	password := flag.String("pass", "", "new password (required unless -pass-stdin)")
	passStdin := flag.Bool("pass-stdin", false, "read new password from stdin (avoids argv exposure)")
	flag.Parse()

	pass := *password
	if *passStdin {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
		if err != nil {
			fmt.Fprintf(os.Stderr, "read password from stdin: %v\n", err)
			os.Exit(1)
		}
		pass = strings.TrimSpace(string(data))
	} else if pass == "" {
		if env := strings.TrimSpace(os.Getenv("FORGEC2_RESET_PASS")); env != "" {
			pass = env
		}
	}

	if *username == "" || pass == "" {
		fmt.Fprintf(os.Stderr, "Usage: reset-password -dsn <dsn> -user <username> (-pass <password> | -pass-stdin)\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if err := checkPasswordPolicy(pass); err != nil {
		fmt.Fprintf(os.Stderr, "password policy: %v\n", err)
		os.Exit(1)
	}

	db, err := sql.Open("postgres", *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "ping db: %v\n", err)
		os.Exit(1)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bcrypt hash failed: %v\n", err)
		os.Exit(1)
	}

	res, err := db.Exec(`UPDATE "users" SET password_hash = $1 WHERE username = $2`, string(hash), *username)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		os.Exit(1)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rows affected: %v\n", err)
		os.Exit(1)
	}
	if rows == 0 {
		fmt.Fprintf(os.Stderr, "user not found: %s\n", *username)
		os.Exit(1)
	}

	fmt.Printf("Password updated for user: %s (rows affected: %d)\n", *username, rows)
}
