// Package bundle collects a stretch of chat messages (including photos,
// stickers, and videos) and packages them into a snapshot accessible via URL:
// browsers get chat-styled HTML, while curl / JSON gets structured data
// (handy for feeding to AI). Media is served by the built-in HTTP server
// under the configured base URL.
package bundle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/bytedance/sonic"
)

// Message is a single message in a bundle. An empty Kind means plain text;
// otherwise it is photo/sticker/video, Media is the media file name (served
// via /m/), and Text is the text or media caption.
// Name is the display name; Username is the optional @ username (shown on
// hover when rendered).
type Message struct {
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Text     string `json:"text,omitempty"`
	Media    string `json:"media,omitempty"`
	Time     int64  `json:"time"`
}

// Bundle is a packaged snapshot of one conversation.
type Bundle struct {
	ID       string    `json:"id"`
	ChatID   int64     `json:"chat_id"`
	Title    string    `json:"title,omitempty"`
	Started  int64     `json:"started"`
	Ended    int64     `json:"ended"`
	Messages []Message `json:"messages"`
}

type store struct {
	mu         sync.RWMutex
	path       string
	mediaDir   string
	baseURL    string
	collecting map[int64]*Bundle
	done       map[string]*Bundle
}

var def = &store{collecting: map[int64]*Bundle{}, done: map[string]*Bundle{}}

// Init loads finished bundles and configures the media directory and the
// externally visible base URL.
func Init(storePath, mediaDir, baseURL string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	def.path = storePath
	def.mediaDir = mediaDir
	def.baseURL = baseURL
	def.collecting = map[int64]*Bundle{}
	def.done = map[string]*Bundle{}
	if mediaDir != "" {
		if err := os.MkdirAll(mediaDir, 0o755); err != nil {
			return err
		}
	}
	return def.load()
}

// Start begins collecting messages in the given chat.
func Start(chatID int64, title string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.collecting[chatID]; ok {
		return fmt.Errorf("已经在收集中,先 /bundle end 结束")
	}
	def.collecting[chatID] = &Bundle{ChatID: chatID, Title: title, Started: time.Now().Unix()}
	return nil
}

// Add appends a text message if the chat is currently collecting (otherwise
// it is ignored).
func Add(chatID int64, name, username, text string) {
	def.mu.Lock()
	defer def.mu.Unlock()
	if b, ok := def.collecting[chatID]; ok {
		b.Messages = append(b.Messages, Message{Name: name, Username: username, Text: text, Time: time.Now().Unix()})
	}
}

// AddMedia, if the chat is currently collecting, writes the downloaded media
// to the media directory and appends a media message.
func AddMedia(chatID int64, name, username, kind, caption string, data []byte, ext string) {
	def.mu.Lock()
	defer def.mu.Unlock()
	b, ok := def.collecting[chatID]
	if !ok {
		return
	}
	fname := newID() + "." + ext
	if def.mediaDir != "" {
		_ = os.WriteFile(filepath.Join(def.mediaDir, fname), data, 0o600)
	}
	b.Messages = append(b.Messages, Message{Name: name, Username: username, Kind: kind, Text: caption, Media: fname, Time: time.Now().Unix()})
}

// End stops collecting, generates an id, moves the bundle to the finished
// set, persists it, and returns it.
func End(chatID int64) (*Bundle, error) {
	def.mu.Lock()
	defer def.mu.Unlock()
	b, ok := def.collecting[chatID]
	if !ok {
		return nil, fmt.Errorf("当前没有在收集,先 /bundle start")
	}
	delete(def.collecting, chatID)
	if len(b.Messages) == 0 {
		return nil, fmt.Errorf("没有收集到任何消息")
	}
	b.ID = newID()
	b.Ended = time.Now().Unix()
	def.done[b.ID] = b
	_ = def.save()
	return b, nil
}

// Cancel aborts an in-progress collection.
func Cancel(chatID int64) bool {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.collecting[chatID]; !ok {
		return false
	}
	delete(def.collecting, chatID)
	return true
}

// Status reports whether the chat is collecting and how many messages have
// been collected so far.
func Status(chatID int64) (collecting bool, count int) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	if b, ok := def.collecting[chatID]; ok {
		return true, len(b.Messages)
	}
	return false, 0
}

// Get returns a finished bundle by id.
func Get(id string) (*Bundle, bool) {
	def.mu.RLock()
	defer def.mu.RUnlock()
	b, ok := def.done[id]
	return b, ok
}

func newID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (s *store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var bs []*Bundle
	if err := sonic.Unmarshal(data, &bs); err != nil {
		return fmt.Errorf("解析 %s: %w", s.path, err)
	}
	for _, b := range bs {
		def.done[b.ID] = b
	}
	return nil
}

func (s *store) save() error {
	if s.path == "" {
		return nil
	}
	bs := make([]*Bundle, 0, len(s.done))
	for _, b := range s.done {
		bs = append(bs, b)
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].Ended < bs[j].Ended })
	data, err := sonic.MarshalIndent(bs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
