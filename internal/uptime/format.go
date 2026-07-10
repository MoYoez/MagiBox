package uptime

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
)

// placeholderRe matches {a.b.0} style references inside a custom template.
var placeholderRe = regexp.MustCompile(`\{([^{}]+)\}`)

// ParseFields splits a comma-separated field spec ("ipgroup, isbanned") into
// trimmed, non-empty dot-paths.
func ParseFields(spec string) []string {
	var out []string
	for _, f := range strings.Split(spec, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Render turns an inbound webhook body into the message pushed to the target.
//
//   - Template set: every {dot.path} placeholder is replaced with the value
//     from the JSON body (missing paths are left intact).
//   - else Fields set: one "field: value" line per field.
//   - else: the JSON "msg" field if present, otherwise the raw body.
//
// A header line identifies the watcher and, when the body carries an Uptime
// Kuma heartbeat status, marks up/down.
func Render(w *Watcher, body []byte) string {
	var data any
	parsed := sonic.Unmarshal(body, &data) == nil

	var out string
	switch {
	case w.Template != "":
		out = renderTemplate(w.Template, data, parsed)
	case len(w.Fields) > 0:
		lines := make([]string, 0, len(w.Fields))
		for _, f := range w.Fields {
			val, ok := lookup(data, f)
			if !parsed {
				val = "<非 JSON 内容>"
			} else if !ok {
				val = "<缺失>"
			}
			lines = append(lines, f+": "+val)
		}
		out = strings.Join(lines, "\n")
	default:
		if msg, ok := lookup(data, "msg"); parsed && ok {
			out = msg
		} else {
			out = strings.TrimSpace(string(body))
		}
	}

	header := "🔔 [" + w.Name + "]"
	if parsed {
		if s, ok := lookup(data, "heartbeat.status"); ok {
			switch s {
			case "0":
				header = "🔴 [" + w.Name + "] Down"
			case "1":
				header = "✅ [" + w.Name + "] Up"
			}
		}
	}
	if out == "" {
		return header
	}
	return header + "\n" + out
}

// renderTemplate replaces each {dot.path} with its extracted value, leaving
// placeholders that don't resolve untouched.
func renderTemplate(tmpl string, data any, parsed bool) string {
	if !parsed {
		return tmpl
	}
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		path := strings.TrimSpace(m[1 : len(m)-1])
		if val, ok := lookup(data, path); ok {
			return val
		}
		return m
	})
}

// lookup walks data with a dot-path ("monitor.name", "arr.0.id"). A numeric
// segment indexes into an array; anything else is an object key.
func lookup(data any, path string) (string, bool) {
	cur := data
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return "", false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return "", false
			}
			cur = node[idx]
		default:
			return "", false
		}
	}
	return stringify(cur), true
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return "null"
	case map[string]any, []any:
		b, _ := sonic.Marshal(t)
		return string(b)
	default:
		return fmt.Sprintf("%v", v)
	}
}
