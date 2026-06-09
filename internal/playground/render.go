package playground

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
)

var (
	placeholderRe = regexp.MustCompile(`\{([^{}]*)\}`)
	segRe         = regexp.MustCompile(`\[([^\]]*)\]`)
	fallbackRe    = regexp.MustCompile(`^(.*?)\s*\|\|\s*"([^"]*)"\s*$`)
)

// Render fills the template with the response. It returns the rendered text;
// if the template contains {body_image}, it also returns the image bytes
// (the text then serves as the image caption).
//
// Supported placeholders:
//
//	{body_raw}                            raw response body
//	{body_code}                           HTTP status code
//	{body_image}                          send the response body as an image
//	{body_jsonlize_spec["a"]["b"][0]}     JSON path lookup (object keys + array indexes)
//	{body_header["Content-Type"]}         read a response header
//	{<expr> || "fallback"}                use the fallback value if evaluation fails
func Render(tmpl string, r *Response) (text string, image []byte) {
	text = placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		inner := strings.TrimSpace(m[1 : len(m)-1])
		expr, fallback, hasFallback := splitFallback(inner)
		if !isKnownExpr(expr) {
			return m // keep unknown placeholders as-is
		}
		val, ok := evalExpr(expr, r, &image)
		if !ok && hasFallback {
			return fallback
		}
		return val
	})
	return strings.TrimSpace(text), image
}

func isKnownExpr(expr string) bool {
	return expr == "body_raw" || expr == "body_code" || expr == "body_image" ||
		strings.HasPrefix(expr, "body_jsonlize_spec") || strings.HasPrefix(expr, "body_header")
}

func evalExpr(expr string, r *Response, image *[]byte) (string, bool) {
	switch {
	case expr == "body_raw":
		return string(r.Body), true
	case expr == "body_code":
		return strconv.Itoa(r.Code), true
	case expr == "body_image":
		*image = r.Body
		return "", true
	case strings.HasPrefix(expr, "body_jsonlize_spec"):
		return jsonPath(r.Body, expr)
	case strings.HasPrefix(expr, "body_header"):
		return headerValue(r.Header, expr)
	default:
		return "", false
	}
}

// splitFallback parses `expr || "fallback"`.
func splitFallback(s string) (expr, fallback string, has bool) {
	if mm := fallbackRe.FindStringSubmatch(s); mm != nil {
		return strings.TrimSpace(mm[1]), mm[2], true
	}
	return s, "", false
}

// jsonPath parses the body as JSON and walks it segment by segment using the
// ["k"] / [n] parts of expr.
func jsonPath(body []byte, expr string) (string, bool) {
	var v any
	if err := sonic.Unmarshal(body, &v); err != nil {
		return "<非JSON响应>", false
	}
	for _, seg := range segRe.FindAllStringSubmatch(expr, -1) {
		s := strings.TrimSpace(seg[1])
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			key := s[1 : len(s)-1]
			m, ok := v.(map[string]any)
			if !ok {
				return "<路径无效>", false
			}
			v, ok = m[key]
			if !ok {
				return "<缺字段:" + key + ">", false
			}
		} else {
			idx, err := strconv.Atoi(s)
			if err != nil {
				return "<索引非法:" + s + ">", false
			}
			arr, ok := v.([]any)
			if !ok {
				return "<非数组>", false
			}
			if idx < 0 || idx >= len(arr) {
				return "<越界:" + s + ">", false
			}
			v = arr[idx]
		}
	}
	return stringify(v), true
}

func headerValue(h http.Header, expr string) (string, bool) {
	mm := segRe.FindStringSubmatch(expr)
	if mm == nil {
		return "<格式错误>", false
	}
	name := strings.TrimSpace(mm[1])
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		name = name[1 : len(name)-1]
	}
	if h == nil {
		return "<无响应头>", false
	}
	v := h.Get(name)
	if v == "" {
		return "<无此响应头:" + name + ">", false
	}
	return v, true
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
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
