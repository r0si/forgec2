package config

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Save writes the config to path, preserving keys this build does not recognise.
//
// Without that preservation a console save is destructive in a way nobody sees: the
// file is rewritten from the in-memory struct, so an option added by a NEWER build
// (or set by hand for one) is deleted the first time an operator changes the port.
// After the upgrade the option is silently back at its default.
//
// Only unknown subtrees are carried over - keys the struct knows are always taken
// from the live config, so a stale value in the old file cannot come back.
func (c *Config) Save(path string) error {
	c.mu.RLock()
	out, err := yaml.Marshal(c)
	c.mu.RUnlock()
	if err != nil {
		return err
	}

	if preserved := c.unknownSubtree(path); len(preserved) > 0 {
		var doc map[string]interface{}
		if err := yaml.Unmarshal(out, &doc); err == nil {
			for _, entry := range preserved {
				insertPath(doc, strings.Split(entry.path, "."), entry.value)
			}
			if merged, merr := yaml.Marshal(doc); merr == nil {
				out = merged
			}
		}
	}
	return os.WriteFile(path, out, 0600)
}

type preservedKey struct {
	path  string
	value interface{}
}

// unknownSubtree reads the config currently on disk and returns the unknown keys it
// carries, as dotted paths with their decoded values.
func (c *Config) unknownSubtree(path string) []preservedKey {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	paths := UnknownConfigKeys(data)
	if len(paths) == 0 {
		return nil
	}
	out := make([]preservedKey, 0, len(paths))
	for _, p := range paths {
		if v, ok := lookupPath(doc, strings.Split(p, ".")); ok {
			out = append(out, preservedKey{path: p, value: v})
		}
	}
	return out
}

func lookupPath(doc map[string]interface{}, parts []string) (interface{}, bool) {
	var current interface{} = doc
	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// insertPath puts value back at the dotted path, creating intermediate maps. A
// parent that exists but is not a map (a scalar the struct does know) is left alone:
// the known value wins.
func insertPath(doc map[string]interface{}, parts []string, value interface{}) {
	if len(parts) == 0 {
		return
	}
	head := parts[0]
	if len(parts) == 1 {
		if _, exists := doc[head]; !exists {
			doc[head] = value
		}
		return
	}
	child, ok := doc[head].(map[string]interface{})
	if !ok {
		if doc[head] != nil {
			return
		}
		child = map[string]interface{}{}
		doc[head] = child
	}
	insertPath(child, parts[1:], value)
}
