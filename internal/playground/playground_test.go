package playground

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	r := &Response{Code: 200, Body: []byte(`{"code":0,"data":{"name":"张三","age":18},"list":["a","b"]}`)}

	text, img := Render(`状态={body_code} 名字={body_jsonlize_spec["data"]["name"]} 年龄={body_jsonlize_spec["data"]["age"]}`, r)
	if img != nil {
		t.Fatal("不应有图片")
	}
	if want := `状态=200 名字=张三 年龄=18`; text != want {
		t.Fatalf("got %q want %q", text, want)
	}

	// array index
	if text, _ := Render(`{body_jsonlize_spec["list"][1]}`, r); text != "b" {
		t.Fatalf("数组下标 got %q", text)
	}
	// fallback value (missing field → fallback)
	if text, _ := Render(`{body_jsonlize_spec["nope"] || "—"}`, r); text != "—" {
		t.Fatalf("兜底 got %q", text)
	}
	// response header
	rh := &Response{Code: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{}`)}
	if text, _ := Render(`{body_header["Content-Type"]}`, rh); text != "application/json" {
		t.Fatalf("响应头 got %q", text)
	}
	// raw body
	if text, _ := Render(`{body_raw}`, r); text != string(r.Body) {
		t.Fatalf("raw 不匹配: %q", text)
	}
	// image
	if _, img := Render(`look {body_image}`, &Response{Body: []byte{1, 2, 3}}); img == nil {
		t.Fatal("应识别 body_image")
	}
	// missing field without fallback → error hint
	if text, _ := Render(`{body_jsonlize_spec["nope"]}`, r); !strings.Contains(text, "缺字段") {
		t.Fatalf("应提示缺字段, got %q", text)
	}
}

func TestStore(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "pg.json")); err != nil {
		t.Fatal(err)
	}
	if err := Create("weather"); err != nil {
		t.Fatal(err)
	}
	if err := Create("weather"); err == nil {
		t.Fatal("重复创建应报错")
	}
	if err := Mutate("weather", func(g *Group) error { g.BaseURL = "https://api.x"; return nil }); err != nil {
		t.Fatal(err)
	}
	g, ok := Get("weather")
	if !ok || g.BaseURL != "https://api.x" {
		t.Fatal("get/mutate 失败")
	}
	if g.Method != "GET" {
		t.Fatalf("默认方式应为 GET, got %q", g.Method)
	}
	if len(List()) != 1 {
		t.Fatal("list 应有 1 个")
	}
	if err := Delete("weather"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Get("weather"); ok {
		t.Fatal("应已删除")
	}
}

func TestAsserts(t *testing.T) {
	r := &Response{Code: 200, Body: []byte(`{"code":0,"msg":"ok"}`)}

	pass := []Assert{
		{Expr: "{body_code}", Op: "==", Want: "200"},
		{Expr: `{body_jsonlize_spec["code"]}`, Op: "==", Want: "0"},
		{Expr: `{body_jsonlize_spec["msg"]}`, Op: "has", Want: "o"},
	}
	if f := EvalAsserts(pass, r); len(f) != 0 {
		t.Fatalf("应全部通过, got %v", f)
	}

	bad := &Response{Code: 500, Body: r.Body}
	fail := []Assert{
		{Expr: "{body_code}", Op: "==", Want: "200"},
		{Expr: `{body_jsonlize_spec["code"]}`, Op: "==", Want: "0"},
	}
	if f := EvalAsserts(fail, bad); len(f) != 1 {
		t.Fatalf("应失败 1 条(状态码), got %v", f)
	}
}

func TestSchedule(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "pg.json")); err != nil {
		t.Fatal(err)
	}
	if err := Create("svc"); err != nil {
		t.Fatal(err)
	}
	if err := SetSchedule("svc", "@every 5m", map[string]string{"city": "shanghai"}); err != nil {
		t.Fatalf("合法 spec 应成功: %v", err)
	}
	if err := SetSchedule("svc", "not a cron", nil); err == nil {
		t.Fatal("非法 spec 应报错")
	}
	g, _ := Get("svc")
	if g.Schedule != "@every 5m" {
		t.Fatalf("schedule = %q, 期望 @every 5m", g.Schedule)
	}
	if g.ScheduleArgs["city"] != "shanghai" {
		t.Fatalf("schedule args = %v, 期望 city=shanghai", g.ScheduleArgs)
	}
	// disabling the check also clears its args
	if err := SetSchedule("svc", "", nil); err != nil {
		t.Fatal(err)
	}
	g, _ = Get("svc")
	if g.Schedule != "" || g.ScheduleArgs != nil {
		t.Fatalf("关闭后应清空: schedule=%q args=%v", g.Schedule, g.ScheduleArgs)
	}
}

func TestDebounce(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "pg.json")); err != nil {
		t.Fatal(err)
	}
	if err := Create("svc"); err != nil {
		t.Fatal(err)
	}
	if err := Mutate("svc", func(g *Group) error {
		g.Asserts = []Assert{{Expr: "{body_code}", Op: "==", Want: "200"}}
		g.FailThreshold = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g, _ := Get("svc")
	fail := []string{"x"}

	if ev := def.classify("svc", g, fail); ev != EventSilent {
		t.Fatalf("第 1 次失败应静默(阈值 2), got %v", ev)
	}
	if ev := def.classify("svc", g, fail); ev != EventFail {
		t.Fatalf("第 2 次失败应告警, got %v", ev)
	}
	if ev := def.classify("svc", g, fail); ev != EventSilent {
		t.Fatalf("持续失败应静默不刷屏, got %v", ev)
	}
	if ev := def.classify("svc", g, nil); ev != EventRecover {
		t.Fatalf("恢复应通知, got %v", ev)
	}
	if ev := def.classify("svc", g, nil); ev != EventSilent {
		t.Fatalf("持续正常应静默, got %v", ev)
	}
}
