package server

import (
	"database/sql"
	"testing"
)

func TestContractDBClosesWithOwningTest(t *testing.T) {
	var connection *sql.DB
	t.Run("owner", func(t *testing.T) {
		database := newContractDB(t)
		var err error
		connection, err = database.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := connection.Ping(); err != nil {
			t.Fatalf("fixture unavailable during owner test: %v", err)
		}
	})
	if connection == nil {
		t.Fatal("owner did not open the database")
	}
	if err := connection.Ping(); err == nil {
		t.Fatal("database connection remained open after owner cleanup")
	}
}
