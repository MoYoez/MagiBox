package bundle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectFlow(t *testing.T) {
	dir := t.TempDir()
	if err := Init(filepath.Join(dir, "bundles.json"), filepath.Join(dir, "media"), "http://host"); err != nil {
		t.Fatal(err)
	}
	const chat = int64(42)
	if err := Start(chat, "测试"); err != nil {
		t.Fatal(err)
	}
	if err := Start(chat, "again"); err == nil {
		t.Fatal("重复 start 应报错")
	}
	Add(chat, "Alice", "alice01", "你好")
	Add(chat, "Bob", "", "在的")
	Add(999, "Ghost", "", "不该被记录") // other chat is not collecting

	if c, n := Status(chat); !c || n != 2 {
		t.Fatalf("status=%v,%d 期望 true,2", c, n)
	}

	b, err := End(chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Messages) != 2 {
		t.Fatalf("消息数 %d 期望 2", len(b.Messages))
	}
	if _, ok := Get(b.ID); !ok {
		t.Fatal("应能按 id 取回")
	}
	if _, err := End(chat); err == nil {
		t.Fatal("已结束后再 end 应报错")
	}
}

func TestServeContentNegotiation(t *testing.T) {
	dir := t.TempDir()
	if err := Init(filepath.Join(dir, "bundles.json"), filepath.Join(dir, "media"), "http://host"); err != nil {
		t.Fatal(err)
	}
	const chat = int64(7)
	_ = Start(chat, "T")
	Add(chat, "A", "a", "<b>hi</b>") // verify HTML escaping works
	b, _ := End(chat)

	srv := httptest.NewServer(Handler())
	defer srv.Close()

	// curl style (no Accept: text/html) → JSON
	resp, err := http.Get(srv.URL + "/b/" + b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("默认应返回 JSON, got %s", ct)
	}
	resp.Body.Close()

	// Browser style (Accept: text/html) → HTML
	req, _ := http.NewRequest("GET", srv.URL+"/b/"+b.ID, nil)
	req.Header.Set("Accept", "text/html")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp2.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("应返回 HTML, got %s", ct)
	}
	resp2.Body.Close()

	// Nonexistent → 404
	resp3, err := http.Get(srv.URL + "/b/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if resp3.StatusCode != http.StatusNotFound {
		t.Fatalf("应 404, got %d", resp3.StatusCode)
	}
	resp3.Body.Close()
}

func TestMedia(t *testing.T) {
	dir := t.TempDir()
	if err := Init(filepath.Join(dir, "b.json"), filepath.Join(dir, "media"), "http://host"); err != nil {
		t.Fatal(err)
	}
	const chat = int64(5)
	_ = Start(chat, "M")
	AddMedia(chat, "A", "a", "photo", "图说", []byte{1, 2, 3}, "jpg")
	b, _ := End(chat)

	if len(b.Messages) != 1 || b.Messages[0].Kind != "photo" {
		t.Fatalf("媒体消息未正确记录: %+v", b.Messages)
	}
	name := b.Messages[0].Media
	if _, err := os.Stat(filepath.Join(dir, "media", name)); err != nil {
		t.Fatalf("媒体文件未写入: %v", err)
	}

	srv := httptest.NewServer(Handler())
	defer srv.Close()

	// /m/ serves media
	resp, err := http.Get(srv.URL + "/m/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("媒体 serve %d", resp.StatusCode)
	}
	resp.Body.Close()

	// media in JSON should be a full URL
	resp2, err := http.Get(srv.URL + "/b/" + b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Messages []struct {
			Media string `json:"media"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&out)
	resp2.Body.Close()
	if len(out.Messages) != 1 || !strings.HasPrefix(out.Messages[0].Media, "http://host/m/") {
		t.Fatalf("JSON media 应为完整 URL, got %+v", out.Messages)
	}
}
