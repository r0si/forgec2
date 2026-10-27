package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// UnknownConfigKeys reports every key in the YAML document that the Config struct
// does not have, as dotted paths ("crypto.require_release_signatur").
//
// The loader cannot simply refuse unknown keys: a config written by a newer build
// carries keys this one does not know, and a hard failure would turn a staged
// rollback into an outage. It cannot stay silent either, and that is the worse
// mistake of the two: a typo in a security control ("require_release_signature")
// is simply dropped, and the operator believes the control is on. So the loader
// WARNS, naming the paths, and the operator sees the mistake on the first start.
func UnknownConfigKeys(data []byte) []string {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// A malformed document is reported by Load itself; nothing to add here.
		return nil
	}
	var out []string
	collectUnknownKeys(reflect.TypeOf(Config{}), doc, "", &out, 0)
	sort.Strings(out)
	return out
}

// collectUnknownKeys walks the struct type alongside the decoded map. depth bounds
// the recursion so a hostile document with deeply nested maps cannot make this
// expensive.
func collectUnknownKeys(t reflect.Type, node map[string]interface{}, prefix string, out *[]string, depth int) {
	if depth > 8 || t == nil {
		return
	}
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}

	fields := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}

	for key, value := range node {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		ft, ok := fields[key]
		if !ok {
			*out = append(*out, path)
			continue
		}
		if child, ok := value.(map[string]interface{}); ok {
			collectUnknownKeys(ft, child, path, out, depth+1)
		}
	}
}

// formatUnknownKeys renders the warning line so every caller logs the same shape.
func formatUnknownKeys(paths []string) string {
	const max = 12
	shown := paths
	suffix := ""
	if len(shown) > max {
		shown = shown[:max]
		suffix = fmt.Sprintf(" (+%d more)", len(paths)-max)
	}
	return fmt.Sprintf("%s%s", strings.Join(shown, ", "), suffix)
}
