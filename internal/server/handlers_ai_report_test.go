package server

import (
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

func TestMITRECoverageCountsPropagatesDatabaseError(t *testing.T) {
	s := newTasksTestServer(t)
	if err := s.db.Migrator().DropTable(&db.Task{}); err != nil {
		t.Fatalf("drop tasks table: %v", err)
	}
	if _, _, _, err := s.mitreCoverageCounts(nil); err == nil {
		t.Fatal("expected MITRE coverage query error")
	}
}

func TestBuildAIMarkdownReportPropagatesDatabaseError(t *testing.T) {
	s := newTasksTestServer(t)
	if err := s.db.Migrator().DropTable(&db.Implant{}); err != nil {
		t.Fatalf("drop implants table: %v", err)
	}
	if _, _, err := s.buildAIMarkdownReport("full", nil); err == nil {
		t.Fatal("expected report query error")
	}
}
