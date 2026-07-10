package uptime

import "testing"

func TestParseFields(t *testing.T) {
	got := ParseFields(" ipgroup, isbanned ,, monitor.name ")
	want := []string{"ipgroup", "isbanned", "monitor.name"}
	if len(got) != len(want) {
		t.Fatalf("ParseFields len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRenderFields(t *testing.T) {
	w := &Watcher{Name: "demo", Fields: []string{"ipgroup", "isbanned", "missing"}}
	body := []byte(`{"ipgroup":"cn-east","isbanned":true}`)
	got := Render(w, body)
	want := "🔔 [demo]\nipgroup: cn-east\nisbanned: true\nmissing: <缺失>"
	if got != want {
		t.Fatalf("Render() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderNestedAndStatusHeader(t *testing.T) {
	w := &Watcher{Name: "svc", Fields: []string{"monitor.name"}}
	body := []byte(`{"heartbeat":{"status":0},"monitor":{"name":"api"}}`)
	got := Render(w, body)
	want := "🔴 [svc] Down\nmonitor.name: api"
	if got != want {
		t.Fatalf("Render() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderTemplate(t *testing.T) {
	w := &Watcher{Name: "t", Template: "组 {ipgroup} 封禁={isbanned} 未知={nope}"}
	body := []byte(`{"ipgroup":"us","isbanned":false}`)
	got := Render(w, body)
	want := "🔔 [t]\n组 us 封禁=false 未知={nope}"
	if got != want {
		t.Fatalf("Render() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderDefaultUsesMsg(t *testing.T) {
	w := &Watcher{Name: "d"}
	body := []byte(`{"msg":"[d] recovered","heartbeat":{"status":1}}`)
	got := Render(w, body)
	want := "✅ [d] Up\n[d] recovered"
	if got != want {
		t.Fatalf("Render() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderNonJSONFields(t *testing.T) {
	w := &Watcher{Name: "x", Fields: []string{"ipgroup"}}
	got := Render(w, []byte("not json"))
	want := "🔔 [x]\nipgroup: <非 JSON 内容>"
	if got != want {
		t.Fatalf("Render() =\n%q\nwant\n%q", got, want)
	}
}
