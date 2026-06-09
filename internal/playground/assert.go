package playground

import (
	"fmt"
	"strings"
)

// Assert is a single assertion against a response. Expr is a placeholder
// expression (evaluated by the renderer) compared with Want using Op.
type Assert struct {
	Expr string `json:"expr"` // e.g. {body_code} or {body_jsonlize_spec["code"]}
	Op   string `json:"op"`   // ==, !=, has
	Want string `json:"want"` // expected value
}

// EvalAsserts evaluates all assertions (AND) against the response and returns
// a list of failure descriptions (empty means all passed).
func EvalAsserts(asserts []Assert, r *Response) []string {
	var fails []string
	for _, a := range asserts {
		got, _, _ := Render(a.Expr, r)
		ok := false
		switch a.Op {
		case "!=":
			ok = got != a.Want
		case "has":
			ok = strings.Contains(got, a.Want)
		default: // "==" and unknown ops both mean equality
			ok = got == a.Want
		}
		if !ok {
			fails = append(fails, fmt.Sprintf("%s %s %q(实际 %q)", a.Expr, a.Op, a.Want, got))
		}
	}
	return fails
}
