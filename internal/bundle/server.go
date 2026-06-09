package bundle

import (
	"html"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// Handler returns the bundle HTTP handler: /b/{id} serves snapshots,
// /m/{name} serves media.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/b/", serveBundle)
	mux.HandleFunc("/m/", serveMedia)
	return mux
}

// Serve starts the bundle HTTP server on addr (blocking).
func Serve(addr string) error {
	return http.ListenAndServe(addr, Handler())
}

func serveBundle(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/b/")
	b, ok := Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Content negotiation: ?format= takes precedence; otherwise go by Accept
	// (browsers ask for html, curl gets json by default).
	format := r.URL.Query().Get("format")
	if format == "" {
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			format = "html"
		} else {
			format = "json"
		}
	}
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = sonic.ConfigDefault.NewEncoder(w).Encode(toOut(b))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(RenderHTML(b)))
}

// serveMedia serves media files (guards against path traversal).
func serveMedia(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/m/")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	def.mu.RLock()
	dir := def.mediaDir
	def.mu.RUnlock()
	if dir == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(dir, name))
}

// --- JSON output: expand media file names into full URLs for AI consumption / external access ---

type outMessage struct {
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Text     string `json:"text,omitempty"`
	Media    string `json:"media,omitempty"`
	Time     int64  `json:"time"`
}

type outBundle struct {
	ID       string       `json:"id"`
	Title    string       `json:"title,omitempty"`
	Started  int64        `json:"started"`
	Ended    int64        `json:"ended"`
	Messages []outMessage `json:"messages"`
}

func toOut(b *Bundle) outBundle {
	def.mu.RLock()
	base := def.baseURL
	def.mu.RUnlock()
	out := outBundle{ID: b.ID, Title: b.Title, Started: b.Started, Ended: b.Ended}
	for _, m := range b.Messages {
		om := outMessage{Name: m.Name, Username: m.Username, Kind: m.Kind, Text: m.Text, Time: m.Time}
		if m.Media != "" {
			om.Media = mediaSrc(base, m.Media)
		}
		out.Messages = append(out.Messages, om)
	}
	return out
}

// mediaSrc resolves a media reference: full URLs / data URIs are returned
// as-is, otherwise it is joined as base+/m/. With an empty base it returns
// a relative /m/ path (same-origin HTML case).
func mediaSrc(base, media string) string {
	if strings.HasPrefix(media, "http://") || strings.HasPrefix(media, "https://") || strings.HasPrefix(media, "data:") {
		return media
	}
	return base + "/m/" + media
}

// --- Chat-style HTML rendering (white background, soft rounded corners) ---

const chatCSS = `
*{box-sizing:border-box}
body{margin:0;background:#fff;color:#343a46;
  font-family:-apple-system,"Segoe UI",Roboto,"PingFang SC","Microsoft YaHei",sans-serif}
.chat{max-width:600px;margin:0 auto;padding:20px 16px 40px}
.datebar{display:flex;justify-content:center;margin:2px 0 6px}
.datebar span{background:#f3f5f8;color:#98a1ae;font-size:12px;
  padding:6px 16px;border-radius:999px}
.from{font-size:13.5px;font-weight:600;color:#4a5160;margin:22px 0 6px 6px;cursor:default}
.msg{margin:0 0 2px}
.card{display:inline-block;background:#f6f7f9;border-radius:18px;
  padding:10px 14px;max-width:86%;overflow-wrap:break-word}
.card.media-card{padding:6px}
.text{font-size:15px;line-height:1.55;white-space:pre-wrap}
.card.media-card .text{padding:4px 8px 2px}
.when{font-size:11px;color:#c2c8d2;margin:3px 0 8px 8px}
.media{display:block;max-width:100%;width:360px;border-radius:13px}
video.media{aspect-ratio:16/9;background:#000}
.sticker{display:block;height:110px}
footer{text-align:center;font-size:11px;color:#d3d8df;margin-top:30px}
`

// RenderHTML renders a bundle as chat HTML with a white background and soft
// rounded corners: a pinned date pill at the top; display names primary
// (hover shows @username); each message in a rounded card with its time
// below; consecutive messages from the same sender show the name only once.
func RenderHTML(b *Bundle) string {
	title := b.Title
	if title == "" {
		title = "Bundle " + b.ID
	}
	var sb strings.Builder
	sb.WriteString(`<!doctype html><html><head><meta charset="utf-8">`)
	sb.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	sb.WriteString("<title>" + html.EscapeString(title) + "</title>")
	sb.WriteString("<style>" + chatCSS + "</style></head><body><div class=\"chat\">")

	// Pinned date pill: shows the date only (exact times appear under each message).
	if b.Started > 0 {
		sb.WriteString(`<div class="datebar"><span>` +
			time.Unix(b.Started, 0).Format("2006-01-02") + `</span></div>`)
	}

	prevKey := "\x00"
	for _, m := range b.Messages {
		key := m.Name + "\x00" + m.Username
		if key != prevKey {
			prevKey = key
			// Name line: display name primary, hover shows @username (if any).
			attr := ""
			if m.Username != "" {
				attr = ` title="@` + html.EscapeString(m.Username) + `"`
			}
			sb.WriteString(`<div class="from"` + attr + `>` + html.EscapeString(m.Name) + `</div>`)
		}

		ts := time.Unix(m.Time, 0).Format("15:04")
		sb.WriteString(`<div class="msg">`)
		if m.Kind == "sticker" {
			sb.WriteString(`<img class="sticker" src="` + html.EscapeString(mediaSrc("", m.Media)) + `">`)
		} else {
			card := "card"
			if m.Kind != "" {
				card += " media-card"
			}
			sb.WriteString(`<div class="` + card + `">`)
			switch m.Kind {
			case "photo":
				sb.WriteString(`<img class="media" src="` + html.EscapeString(mediaSrc("", m.Media)) + `">`)
			case "video":
				sb.WriteString(`<video class="media" src="` + html.EscapeString(mediaSrc("", m.Media)) + `" controls></video>`)
			}
			if m.Text != "" {
				sb.WriteString(`<div class="text">` + html.EscapeString(m.Text) + `</div>`)
			}
			sb.WriteString(`</div>`)
		}
		// Each message's time goes below its card.
		sb.WriteString(`<div class="when">` + ts + `</div></div>`)
	}

	sb.WriteString(`<footer>📦 Magic Complete</footer>`)
	sb.WriteString(`</div></body></html>`)
	return sb.String()
}
