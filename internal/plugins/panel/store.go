package panel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/bytedance/sonic"
)

// store holds the panel's persistent signing secret and the set of pending
// one-time login codes. Codes are consumed on first successful login.
type store struct {
	mu     sync.Mutex
	path   string
	secret []byte
	codes  map[string]int64 // code -> created unix (pending, single-use)
}

var def = &store{codes: map[string]int64{}}

type fileModel struct {
	Secret string           `json:"secret"`
	Codes  map[string]int64 `json:"codes"`
}

// Init loads the panel store, generating a random signing secret on first run.
func Init(path string) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	def.path = path
	if err := def.load(); err != nil {
		return err
	}
	if len(def.secret) == 0 {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		def.secret = b
		if err := def.save(); err != nil {
			return err
		}
	}
	return nil
}

func secret() []byte {
	def.mu.Lock()
	defer def.mu.Unlock()
	return def.secret
}

// NewCode generates, stores, and returns a one-time login code.
func NewCode(nowUnix int64) (string, error) {
	code, err := randomCode()
	if err != nil {
		return "", err
	}
	def.mu.Lock()
	defer def.mu.Unlock()
	def.codes[code] = nowUnix
	if err := def.save(); err != nil {
		return "", err
	}
	return code, nil
}

// ConsumeCode removes a pending code and reports whether it existed (single use).
func ConsumeCode(code string) bool {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.codes[code]; !ok {
		return false
	}
	delete(def.codes, code)
	_ = def.save()
	return true
}

// ListCodes returns the pending (unused) codes, sorted.
func ListCodes() []string {
	def.mu.Lock()
	defer def.mu.Unlock()
	out := make([]string, 0, len(def.codes))
	for c := range def.codes {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Revoke drops a pending code, reporting whether it existed.
func Revoke(code string) bool {
	def.mu.Lock()
	defer def.mu.Unlock()
	if _, ok := def.codes[code]; !ok {
		return false
	}
	delete(def.codes, code)
	_ = def.save()
	return true
}

// randomCode returns a typeable single-use code like "a1b2-c3d4-e5f6-7890".
func randomCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	h := hex.EncodeToString(b)
	return h[0:4] + "-" + h[4:8] + "-" + h[8:12] + "-" + h[12:16], nil
}

func (s *store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var m fileModel
	if err := sonic.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("解析 %s: %w", s.path, err)
	}
	if m.Secret != "" {
		b, err := hex.DecodeString(m.Secret)
		if err != nil {
			return fmt.Errorf("panel secret 解析: %w", err)
		}
		s.secret = b
	}
	if m.Codes != nil {
		s.codes = m.Codes
	}
	return nil
}

func (s *store) save() error {
	m := fileModel{Secret: hex.EncodeToString(s.secret), Codes: s.codes}
	data, err := sonic.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
