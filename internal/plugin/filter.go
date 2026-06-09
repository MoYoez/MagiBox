package plugin

import "strings"

// Filter modes for switching plugins on and off.
const (
	ModeBlacklist = "blacklist" // all plugins enabled except the listed ones (default)
	ModeWhitelist = "whitelist" // only the listed plugins are enabled
)

var (
	filterMode = ModeBlacklist
	filterSet  = map[string]bool{}
)

// SetFilter configures the plugin on/off filter and must be called before
// Setup. In blacklist mode the list names disabled plugins; in whitelist
// mode it names the only enabled ones. Unknown mode falls back to blacklist.
func SetFilter(mode string, names []string) {
	if strings.EqualFold(strings.TrimSpace(mode), ModeWhitelist) {
		filterMode = ModeWhitelist
	} else {
		filterMode = ModeBlacklist
	}
	filterSet = map[string]bool{}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			filterSet[n] = true
		}
	}
}

// Enabled reports whether a plugin passes the current filter.
func Enabled(name string) bool {
	if filterMode == ModeWhitelist {
		return filterSet[name]
	}
	return !filterSet[name]
}
