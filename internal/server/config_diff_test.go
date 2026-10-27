package server

import (
	"reflect"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
)

// TestDiffConfigReportsNoDuplicates pins a copy-paste bug that was live: the
// crypto.require_release_signature condition appeared twice, so a change to a
// security control was reported twice and the "N fields changed" count that goes to
// the log (and the audit trail) was inflated.
//
// The check is generic: flip ONE field at a time and require that each name appears
// at most once in the combined result. That also catches a future field compared by
// two different conditions.
func TestDiffConfigReportsNoDuplicates(t *testing.T) {
	// Every config section that participates in the diff, with a mutator that changes
	// one scalar in it.
	mutations := map[string]func(*config.Config){
		"server.port":                      func(c *config.Config) { c.Server.Port++ },
		"server.beacon_key":                func(c *config.Config) { c.Server.BeaconKey = "beacon-changed" },
		"server.jwt_secret":                func(c *config.Config) { c.Server.JWTSecret = "jwt-changed" },
		"crypto.loot_key":                  func(c *config.Config) { c.Crypto.LootKey = "loot-changed" },
		"crypto.extc2_key":                 func(c *config.Config) { c.Crypto.ExtC2Key = "extc2-changed" },
		"crypto.csrf_key":                  func(c *config.Config) { c.Crypto.CsrfKey = "csrf-changed" },
		"crypto.totp_key":                  func(c *config.Config) { c.Crypto.TotpKey = "totp-changed" },
		"crypto.update_signing_key":        func(c *config.Config) { c.Crypto.UpdateSigningKey = "key-changed" },
		"crypto.require_release_signature": func(c *config.Config) { c.Crypto.RequireReleaseSignature = true },
		// The four sections the hand-written diff never mentioned: a change to any of
		// them was reported as "no values differ" and the reload was skipped.
		"plugins":  func(c *config.Config) { c.Plugins.RequireSigned = !c.Plugins.RequireSigned },
		"backup":   func(c *config.Config) { c.Backup.Retain = c.Backup.Retain + 1 },
		"webrtc":   func(c *config.Config) { c.WebRTC.Enabled = !c.WebRTC.Enabled },
		"protocol": func(c *config.Config) { c.Protocol.CompatWindow++ },
	}
	for name, mutate := range mutations {
		old := config.DefaultConfig()
		newer := config.DefaultConfig()
		mutate(newer)

		hot, static := diffConfig(old, newer)
		all := append(append([]string{}, hot...), static...)
		if len(all) == 0 {
			t.Fatalf("%s: changing it produced no diff entry, so a reload would be skipped", name)
		}
		seen := map[string]int{}
		for _, f := range all {
			seen[f]++
		}
		for field, count := range seen {
			if count > 1 {
				t.Fatalf("%s: field %q reported %d times", name, field, count)
			}
		}
		if _, ok := seen[name]; !ok {
			t.Fatalf("%s: change reported as %v", name, all)
		}
	}
}

// TestCopyFromCoversEverySection pins the invariant CopyFrom's own comment states:
// "Every top-level section must be listed here, otherwise hot-reload silently keeps
// stale values for the omitted sections." A newly added section that nobody adds to
// CopyFrom would leave the live config half-updated after a reload.
func TestCopyFromCoversEverySection(t *testing.T) {
	src := config.DefaultConfig()
	// Give every top-level section a distinguishable state where that is meaningful,
	// then copy and compare with reflection: any section CopyFrom forgot stays equal to
	// the destination's default and the comparison catches it.
	dst := config.DefaultConfig()

	// Mutate a scalar inside every section we can reach generically: set every exported
	// string field to a marker, which is enough to make the section differ.
	markSections(t, src)

	dst.CopyFrom(src)

	sv := reflect.ValueOf(src).Elem()
	dv := reflect.ValueOf(dst).Elem()
	for i := 0; i < sv.NumField(); i++ {
		name := sv.Type().Field(i).Name
		if !sv.Type().Field(i).IsExported() {
			continue
		}
		// ConfigPath is the file this config was read from, not a section.
		if name == "ConfigPath" {
			continue
		}
		if !reflect.DeepEqual(sv.Field(i).Interface(), dv.Field(i).Interface()) {
			t.Fatalf("CopyFrom left section %q stale", name)
		}
	}
}

// markSections sets a marker on the exported string fields of a value and its
// nested structs (bounded depth), so every section differs from a default config.
func markSections(t *testing.T, cfg *config.Config) {
	t.Helper()
	markValue(t, reflect.ValueOf(cfg).Elem(), 0)
}

func markValue(t *testing.T, v reflect.Value, depth int) {
	t.Helper()
	if depth > 3 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		markValue(t, v.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if !f.CanSet() {
				continue
			}
			switch f.Kind() {
			case reflect.String:
				// Mutex-guarded configs are copied by their own method; a plain
				// assignment here is only for the test's own throwaway value.
				f.SetString("marker-" + v.Type().Field(i).Name)
			case reflect.Bool:
				f.SetBool(!f.Bool())
			case reflect.Int, reflect.Int64:
				f.SetInt(f.Int() + 1)
			case reflect.Struct, reflect.Ptr:
				markValue(t, f, depth+1)
			}
		}
	}
}
