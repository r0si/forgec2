package server

import (
	"archive/zip"
	"encoding/json"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// bundleMemberDescription is one row of MANIFEST.json.
type bundleMemberDescription struct {
	Name        string `json:"name"`
	Purpose     string `json:"purpose"`
	Source      string `json:"source"`
	Sensitivity string `json:"sensitivity"`
}

// describeBundleMember answers, for each archive member, what it is, where it came from and how it may be shared.
//
// The manifest exists because a bundle outlives the conversation that produced it: a receiver who was not there needs
// to know that audit.ndjson is the verifiable record, that credentials.json deliberately carries no secrets, and that
// iocs.json is bounded differently from the rest. An unknown member is described honestly rather than guessed at.
func describeBundleMember(name string) bundleMemberDescription {
	switch name {
	case "summary.md":
		return bundleMemberDescription{name, "the exercise in one page: window, fleet, outcomes, audited actions",
			"/api/engagements/:id/summary/export", "shareable"}
	case "timeline.md":
		return bundleMemberDescription{name, "audited actions and task outcomes interleaved by time",
			"derived from audit.ndjson and tasks.csv", "shareable"}
	case "tasks.csv":
		return bundleMemberDescription{name, "the tasks of the engagement hosts inside the window",
			"/api/tasks/export (default columns)", "contains collected output: review before sharing"}
	case "audit.ndjson":
		return bundleMemberDescription{name, "the audited actions of the window, with both hashes",
			"/api/audit/export", "verifiable offline; share in full so a gap is never mistaken for a filter"}
	case "agents.json":
		return bundleMemberDescription{name, "the hosts the engagement covered", "implants table", "shareable"}
	case "credentials.json":
		return bundleMemberDescription{name, "credentials found: METADATA ONLY, no passwords or hashes",
			"credential store", "metadata only by design; retrieve secrets in the console where access is audited"}
	case "iocs.json":
		return bundleMemberDescription{name, "indicators extracted from the engagement hosts",
			"/api/iocs/export (same extractor)", "bound is a day count spanning the engagement, not the exact window"}
	case "MANIFEST.json":
		return bundleMemberDescription{name, "this file", "generated", "shareable"}
	default:
		return bundleMemberDescription{name, "undescribed member", "unknown", "review before sharing"}
	}
}

// writeBundleManifest emits MANIFEST.json from the list of members that made it into the archive.
//
// It is generated from that list rather than from a hand-written catalogue, so a member cannot be added without
// appearing here - and the test asserts the two directions agree.
func writeBundleManifest(archive *zip.Writer, engagement db.Engagement, windowEnd, now time.Time, written []string) error {
	members := make([]bundleMemberDescription, 0, len(written))
	for _, name := range written {
		members = append(members, describeBundleMember(name))
	}
	return writeJSONMember(archive, "MANIFEST.json", gin.H{
		"engagement": gin.H{
			"id":         engagement.ID,
			"name":       engagement.Name,
			"starts_at":  engagement.StartsAt.UTC().Format(time.RFC3339),
			"ends_at":    engagement.EndsAt.UTC().Format(time.RFC3339),
			"window_end": windowEnd.UTC().Format(time.RFC3339),
		},
		"generated_at": now.Format(time.RFC3339),
		"members":      members,
		"note":         "each member names its source and its sensitivity; credentials.json is metadata only by design",
	})
}

// writeJSONMember encodes value as an indented JSON member of the archive.
func writeJSONMember(archive *zip.Writer, name string, value interface{}) error {
	f, err := archive.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
