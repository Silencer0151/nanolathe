// Package i18n translates the native interface's English text
// (docs/DESIGN_I18N.md). Keys are the English strings the interface already
// draws, as retail's translate.tdf keys its sections: a string with no entry
// is shown as written, so new or reworded text stays readable in English
// until it is translated.
//
// A key containing {} or {#} is a pattern: each {} matches a run of text and
// each {#} a number, and the translation places them at its own {} in order,
// or at {1}, {2}... by number. A captured run is translated too, so a
// sentence assembled from translated parts reads as one. Translation is
// presentation only; nothing the simulation computes reads it.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// English is the source language: no catalog, every string as written.
const English = "english"

// Languages lists the selectable languages, English first, with each one's
// own name for itself.
var Languages = []struct{ Code, Name string }{
	{English, "English"},
	{"ukrainian", "Українська"},
	{"chinese", "简体中文"},
}

//go:embed locales/*.json
var locales embed.FS

type pattern struct {
	re          *regexp.Regexp
	prefix      string
	translation string
}

type catalog struct {
	language string
	exact    map[string]string // lookup only; never ranged
	patterns []pattern

	mu    sync.Mutex
	cache map[string]string // lookup only; never ranged
}

var current atomic.Pointer[catalog]

// SetLanguage selects the language T translates into. English, an empty
// code, or a code with no catalog selects no translation; the error reports
// a catalog that would not load.
func SetLanguage(code string) error {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || code == English {
		current.Store(nil)
		return nil
	}
	if c := current.Load(); c != nil && c.language == code {
		return nil
	}
	c, err := load(code)
	if err != nil {
		current.Store(nil)
		return err
	}
	current.Store(c)
	return nil
}

// Language is the selected language's code.
func Language() string {
	if c := current.Load(); c != nil {
		return c.language
	}
	return English
}

// Known reports whether code names a selectable language.
func Known(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	for _, l := range Languages {
		if l.Code == code {
			return true
		}
	}
	return false
}

// T is s in the selected language, or s itself when it has no entry.
func T(s string) string {
	c := current.Load()
	if c == nil || s == "" {
		return s
	}
	return c.translate(s, 0)
}

const cacheLimit = 4096

func (c *catalog) translate(s string, depth int) string {
	if v, ok := c.exact[s]; ok {
		return v
	}
	if depth > 2 {
		return s
	}
	if depth == 0 {
		c.mu.Lock()
		v, ok := c.cache[s]
		c.mu.Unlock()
		if ok {
			return v
		}
	}
	out := s
	for _, p := range c.patterns {
		if !strings.HasPrefix(s, p.prefix) {
			continue
		}
		m := p.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		parts := make([]string, len(m)-1)
		for i, part := range m[1:] {
			parts[i] = c.translate(part, depth+1)
		}
		out = fill(p.translation, parts)
		break
	}
	// Only whole runs are kept: a part translated inside a longer run stops
	// at the nesting limit, which the same text set alone may not reach.
	if depth > 0 {
		return out
	}
	c.mu.Lock()
	if len(c.cache) >= cacheLimit {
		c.cache = make(map[string]string)
	}
	c.cache[s] = out
	c.mu.Unlock()
	return out
}

var placeholder = regexp.MustCompile(`\{(\d*)\}`)

// hole is a pattern key's placeholder: {} for a run of text, {#} for a number.
var hole = regexp.MustCompile(`\{#?\}`)

// fill places parts at the translation's placeholders: {} takes the next
// part in order, {n} the nth.
func fill(translation string, parts []string) string {
	next := 0
	return placeholder.ReplaceAllStringFunc(translation, func(tag string) string {
		i := next
		if n, err := strconv.Atoi(tag[1 : len(tag)-1]); err == nil {
			i = n - 1
		} else {
			next++
		}
		if i < 0 || i >= len(parts) {
			return tag
		}
		return parts[i]
	})
}

func load(code string) (*catalog, error) {
	data, err := locales.ReadFile("locales/" + code + ".json")
	if err != nil {
		return nil, fmt.Errorf("nanolathe: language %q: logical path i18n/locales/%s.json, providers searched [embedded], expected a translation catalog", code, code)
	}
	var entries map[string]string
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("nanolathe: language %q catalog: %v: logical path i18n/locales/%s.json, providers searched [embedded], expected a JSON object of strings", code, err, code)
	}
	return build(code, entries), nil
}

// build compiles a catalog's entries: exact strings and {} patterns.
func build(code string, entries map[string]string) *catalog {
	c := &catalog{language: code, exact: make(map[string]string, len(entries)), cache: make(map[string]string)}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	// Longer patterns first, so a specific sentence wins over a general one.
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		v := entries[k]
		if v == "" {
			continue
		}
		holes := hole.FindAllStringIndex(k, -1)
		if holes == nil {
			c.exact[k] = v
			continue
		}
		var re strings.Builder
		re.WriteString("^")
		last := 0
		for _, h := range holes {
			re.WriteString(regexp.QuoteMeta(k[last:h[0]]))
			if k[h[0]:h[1]] == "{#}" {
				re.WriteString(`(-?[0-9][0-9.,]*)`)
			} else {
				re.WriteString(`(.+?)`)
			}
			last = h[1]
		}
		re.WriteString(regexp.QuoteMeta(k[last:]) + "$")
		c.patterns = append(c.patterns, pattern{
			re:          regexp.MustCompile(re.String()),
			prefix:      k[:holes[0][0]],
			translation: v,
		})
	}
	return c
}
