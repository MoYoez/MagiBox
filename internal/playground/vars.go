package playground

import (
	"os"
	"regexp"
	"sort"
	"sync"

	"github.com/bytedance/sonic"
)

var varRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

var varStore = struct {
	mu   sync.RWMutex
	path string
	m    map[string]string
}{m: map[string]string{}}

// InitVars loads the persisted variable table (and resets memory).
func InitVars(path string) error {
	varStore.mu.Lock()
	defer varStore.mu.Unlock()
	varStore.path = path
	varStore.m = map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return sonic.Unmarshal(data, &varStore.m)
}

// SetVar sets a variable and persists it.
func SetVar(key, val string) error {
	varStore.mu.Lock()
	defer varStore.mu.Unlock()
	varStore.m[key] = val
	return saveVars()
}

// DelVar deletes a variable and persists the change.
func DelVar(key string) error {
	varStore.mu.Lock()
	defer varStore.mu.Unlock()
	delete(varStore.m, key)
	return saveVars()
}

// Vars returns all variable names (sorted).
func Vars() []string {
	varStore.mu.RLock()
	defer varStore.mu.RUnlock()
	keys := make([]string, 0, len(varStore.m))
	for k := range varStore.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// VarValue returns a variable's current value (used for masked display).
func VarValue(key string) string {
	varStore.mu.RLock()
	defer varStore.mu.RUnlock()
	return varStore.m[key]
}

func saveVars() error {
	if varStore.path == "" {
		return nil
	}
	data, err := sonic.MarshalIndent(varStore.m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(varStore.path, data, 0o600)
}

// resolveVarWith resolves a variable: overrides (runtime args) first, then
// the persistent variable table, then environment variables.
func resolveVarWith(name string, overrides map[string]string) (string, bool) {
	if overrides != nil {
		if v, ok := overrides[name]; ok {
			return v, true
		}
	}
	varStore.mu.RLock()
	v, ok := varStore.m[name]
	varStore.mu.RUnlock()
	if ok {
		return v, true
	}
	if e := os.Getenv(name); e != "" {
		return e, true
	}
	return "", false
}

// expandVarsWith replaces {{name}} in a string with variable values;
// overrides take precedence, and undefined names are kept as-is.
// Note the double braces, which do not clash with the response template's
// single-brace {body_*} placeholders.
func expandVarsWith(s string, overrides map[string]string) string {
	return varRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-2]
		if v, ok := resolveVarWith(name, overrides); ok {
			return v
		}
		return m
	})
}

// expandVars is the convenience form without runtime args.
func expandVars(s string) string { return expandVarsWith(s, nil) }

// MissingVars returns the {{var}} names referenced by the group's request
// fields (baseurl, endpoint, headers, body) that cannot be resolved from
// overrides, the variable table, or the environment — i.e. the arguments
// the caller still has to fill in manually.
func MissingVars(g *Group, overrides map[string]string) []string {
	fields := []string{g.BaseURL, g.Endpoint, g.Body}
	for _, v := range g.Headers {
		fields = append(fields, v)
	}
	seen := map[string]bool{}
	var missing []string
	for _, f := range fields {
		for _, m := range varRe.FindAllStringSubmatch(f, -1) {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			if _, ok := resolveVarWith(name, overrides); !ok {
				missing = append(missing, name)
			}
		}
	}
	sort.Strings(missing)
	return missing
}
