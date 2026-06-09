// Package auth provides basic role-based access control (RBAC).
//
// Three role levels: user < admin < owner. The first user binds as owner
// (/bind) via a one-time pairing code printed at startup; the owner can
// promote/demote others (/promote, /demote). Roles are persisted to disk
// and survive restarts.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
)

// Role is a permission level; higher values grant more privileges.
type Role int

const (
	RoleUser  Role = iota // default: anyone
	RoleAdmin             // administrator
	RoleOwner             // owner (unique, bound via pairing code)
)

func (r Role) String() string {
	switch r {
	case RoleOwner:
		return "owner"
	case RoleAdmin:
		return "admin"
	default:
		return "user"
	}
}

// ParseRole parses a role name (owner/admin/user).
func ParseRole(s string) (Role, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "owner":
		return RoleOwner, true
	case "admin":
		return RoleAdmin, true
	case "user":
		return RoleUser, true
	}
	return RoleUser, false
}

type store struct {
	mu    sync.RWMutex
	path  string
	roles map[int64]Role // only roles > user are recorded; absent means user
	code  string         // owner pairing code; empty means binding is closed
}

var def = &store{roles: map[int64]Role{}}

// Init loads persisted roles; if there is no owner yet, it generates a
// one-time pairing code and prints it to the terminal.
func Init(path string) error {
	def.mu.Lock()
	defer def.mu.Unlock()

	def.path = path
	if err := def.load(); err != nil {
		return err
	}

	if !def.hasOwner() {
		def.code = newCode()
		log.Printf("[auth] 还没有 owner。把下面这行发给你的 bot 完成绑定:\n\n    /bind %s\n", def.code)
	} else {
		log.Printf("[auth] 已加载 %d 个特权用户", len(def.roles))
	}
	return nil
}

// Bind validates the pairing code; on success it binds chatID as owner,
// persists the change, and invalidates the code.
func Bind(code string, chatID int64) bool {
	def.mu.Lock()
	defer def.mu.Unlock()

	if def.code == "" || code != def.code {
		return false
	}
	def.roles[chatID] = RoleOwner
	def.code = ""
	if err := def.save(); err != nil {
		log.Printf("[auth] 持久化失败: %v", err)
	}
	log.Printf("[auth] 已绑定 owner chat id=%d", chatID)
	return true
}

// RoleOf returns the role of chatID (defaults to RoleUser).
func RoleOf(chatID int64) Role {
	def.mu.RLock()
	defer def.mu.RUnlock()
	return def.roles[chatID]
}

// Has reports whether the role of chatID is >= min.
func Has(chatID int64, min Role) bool {
	return RoleOf(chatID) >= min
}

// SetRole sets the role of chatID and persists it. When role == RoleUser
// the record is deleted.
func SetRole(chatID int64, role Role) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if role == RoleUser {
		delete(def.roles, chatID)
	} else {
		def.roles[chatID] = role
	}
	return def.save()
}

// IDs returns all chat ids whose role is >= min.
func IDs(min Role) []int64 {
	def.mu.RLock()
	defer def.mu.RUnlock()
	var ids []int64
	for id, r := range def.roles {
		if r >= min {
			ids = append(ids, id)
		}
	}
	return ids
}

// Member is a single role record.
type Member struct {
	ID   int64
	Role Role
}

// Members returns all privileged users (role > user), sorted by role
// descending, then id ascending.
func Members() []Member {
	def.mu.RLock()
	defer def.mu.RUnlock()
	ms := make([]Member, 0, len(def.roles))
	for id, r := range def.roles {
		ms = append(ms, Member{ID: id, Role: r})
	}
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].Role != ms[j].Role {
			return ms[i].Role > ms[j].Role
		}
		return ms[i].ID < ms[j].ID
	})
	return ms
}

func (s *store) hasOwner() bool {
	for _, r := range s.roles {
		if r == RoleOwner {
			return true
		}
	}
	return false
}

func newCode() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- Persistence (JSON); load/save are called with the lock held and do no locking themselves ---

type entry struct {
	ID   int64 `json:"id"`
	Role Role  `json:"role"`
}

type fileModel struct {
	Members []entry `json:"members"`
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
	for _, e := range m.Members {
		s.roles[e.ID] = e.Role
	}
	return nil
}

func (s *store) save() error {
	m := fileModel{}
	for id, r := range s.roles {
		m.Members = append(m.Members, entry{ID: id, Role: r})
	}
	data, err := sonic.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
