package server

import (
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
	// SHA256 is the digest of the member as it was written into the archive, so a receiver can prove that the file
	// survived email, a copy and a re-zip.
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
}

// describeBundleMember answers, for each archive member, what it is, where it came from and how it may be shared.
//
// The manifest exists because a bundle outlives the conversation that produced it: a receiver who was not there needs
// to know that audit.ndjson is the verifiable record, that credentials.json deliberately carries no secrets, and that
// iocs.json is bounded differently from the rest. An unknown member is described honestly rather than guessed at.
func describeBundleMember(name string) bundleMemberDescription {
	describe := func(purpose, source, sensitivity string) bundleMemberDescription {
		return bundleMemberDescription{
			Name: name, Purpose: purpose, Source: source, Sensitivity: sensitivity,
		}
	}
	switch name {
	case "summary.md":
		return describe("the exercise in one page: window, fleet, outcomes, audited actions",
			"/api/engagements/:id/summary/export", "shareable")
	case "timeline.md":
		return describe("audited actions and task outcomes interleaved by time",
			"derived from audit.ndjson and tasks.csv", "shareable")
	case "tasks.csv":
		return describe("the tasks of the engagement hosts inside the window",
			"/api/tasks/export (default columns)", "contains collected output: review before sharing")
	case "audit.ndjson":
		return describe("the audited actions of the window, with both hashes",
			"/api/audit/export", "verifiable offline; share in full so a gap is never mistaken for a filter")
	case "agents.json":
		return describe("the hosts the engagement covered", "implants table", "shareable")
	case "credentials.json":
		return describe("credentials found: METADATA ONLY, no passwords or hashes",
			"credential store", "metadata only by design; retrieve secrets in the console where access is audited")
	case "iocs.json":
		return describe("indicators extracted from the engagement hosts",
			"/api/iocs/export (same extractor)", "bound is a day count spanning the engagement, not the exact window")
	case "MANIFEST.json":
		return describe("this file", "generated", "shareable")
	default:
		return describe("undescribed member", "unknown", "review before sharing")
	}
}

// writeBundleManifest emits MANIFEST.json from the list of members that made it into the archive.
//
// It is generated from that list rather than from a hand-written catalogue, so a member cannot be added without
// appearing here - and the test asserts the two directions agree. Each entry carries the digest the recorder captured
// while the member was written, so the manifest describes what shipped rather than what was intended.
func writeBundleManifest(archive bundleWriter, recorder *hashingBundleWriter, engagement db.Engagement,
	windowEnd, now time.Time, written []string) error {
	members := make([]bundleMemberDescription, 0, len(written))
	for _, name := range written {
		description := describeBundleMember(name)
		if digest, size := recorder.digestOf(name); digest != "" {
			description.SHA256 = digest
			description.Bytes = size
		}
		members = append(members, description)
	}
	generatedAt := now.Format(time.RFC3339)
	payload := bundleManifestPayload(engagement.ID, generatedAt, members)
	signature := signBundleManifest(bundleSigner(), payload, len(members))
	return writeJSONMember(archive, "MANIFEST.json", gin.H{
		"engagement": gin.H{
			"id":         engagement.ID,
			"name":       engagement.Name,
			"starts_at":  engagement.StartsAt.UTC().Format(time.RFC3339),
			"ends_at":    engagement.EndsAt.UTC().Format(time.RFC3339),
			"window_end": windowEnd.UTC().Format(time.RFC3339),
		},
		"generated_at": generatedAt,
		"members":      members,
		"signed":       signature != nil,
		"signature":    signature,
		"note": "each member names its source and its sensitivity; the sha256 is of the member exactly as written, " +
			"and MANIFEST.json is the only file without one (it cannot contain its own digest)",
	})
}

// writeJSONMember encodes value as an indented JSON member of the archive.
func writeJSONMember(archive bundleWriter, name string, value interface{}) error {
	f, err := archive.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
