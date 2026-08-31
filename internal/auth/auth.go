// Package auth provides basic role-based access control (RBAC).
//
// Three role levels: user < admin < owner. Business permissions are stored
// independently from roles and can be stacked on ordinary users. The first
// user binds as owner (/bind) via a one-time pairing code printed at startup;
// the owner can manage roles and permissions. Authorization state is persisted
// to disk and survives restarts.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
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

// Permission is a composable business capability such as "auth:aff".
type Permission string

var permissionPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?::[a-z][a-z0-9_-]*)+$`)

func (p Permission) String() string { return string(p) }

// ParsePermission validates and canonicalizes a permission name.
func ParsePermission(value string) (Permission, bool) {
	permission := Permission(strings.ToLower(strings.TrimSpace(value)))
	return permission, permissionPattern.MatchString(permission.String())
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
	mu          sync.RWMutex
	path        string
	roles       map[int64]Role // only roles > user are recorded; absent means user
	permissions map[int64]map[Permission]struct{}
	code        string // owner pairing code; empty means binding is closed
}

var def = &store{
	roles:       map[int64]Role{},
	permissions: map[int64]map[Permission]struct{}{},
}

// Init loads persisted roles; if there is no owner yet, it generates a
// one-time pairing code and prints it to the terminal.
func Init(path string) error {
	def.mu.Lock()
	defer def.mu.Unlock()

	def.path = path
	def.roles = map[int64]Role{}
	def.permissions = map[int64]map[Permission]struct{}{}
	def.code = ""
	if err := def.load(); err != nil {
		return err
	}

	if !def.hasOwner() {
		def.code = newCode()
		log.Printf("[auth] 还没有 owner。把下面这行发给你的 bot 完成绑定:\n\n    /bind %s\n", def.code)
	} else {
		log.Printf("[auth] 已加载 %d 个授权成员", def.authorizationCount())
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

// HasPermission reports whether chatID has an explicit business permission.
// Admin and owner roles inherit every business permission for compatibility.
func HasPermission(chatID int64, value Permission) bool {
	permission, ok := ParsePermission(value.String())
	if !ok {
		return false
	}
	def.mu.RLock()
	defer def.mu.RUnlock()
	if def.roles[chatID] >= RoleAdmin {
		return true
	}
	_, ok = def.permissions[chatID][permission]
	return ok
}

// Permissions returns the explicitly granted permissions for chatID.
func Permissions(chatID int64) []Permission {
	def.mu.RLock()
	defer def.mu.RUnlock()
	permissions := make([]Permission, 0, len(def.permissions[chatID]))
	for permission := range def.permissions[chatID] {
		permissions = append(permissions, permission)
	}
	sort.Slice(permissions, func(i, j int) bool { return permissions[i] < permissions[j] })
	return permissions
}

// SetRole sets the role of chatID and persists it. RoleUser removes only the
// role while preserving explicitly granted permissions.
func SetRole(chatID int64, role Role) error {
	def.mu.Lock()
	defer def.mu.Unlock()
	if chatID == 0 {
		return fmt.Errorf("auth: chat id must not be zero")
	}
	if role < RoleUser || role > RoleOwner {
		return fmt.Errorf("auth: invalid role %d", role)
	}
	if role == RoleUser {
		delete(def.roles, chatID)
	} else {
		def.roles[chatID] = role
	}
	return def.save()
}

// GrantPermissions adds permissions without replacing existing grants.
func GrantPermissions(chatID int64, values ...Permission) error {
	permissions, err := normalizedPermissions(values)
	if err != nil {
		return err
	}
	if chatID == 0 {
		return fmt.Errorf("auth: chat id must not be zero")
	}
	def.mu.Lock()
	defer def.mu.Unlock()
	if def.permissions[chatID] == nil {
		def.permissions[chatID] = make(map[Permission]struct{}, len(permissions))
	}
	for _, permission := range permissions {
		def.permissions[chatID][permission] = struct{}{}
	}
	return def.save()
}

// RevokePermissions removes only the requested permissions and preserves roles
// and every other explicit permission.
func RevokePermissions(chatID int64, values ...Permission) error {
	permissions, err := normalizedPermissions(values)
	if err != nil {
		return err
	}
	if chatID == 0 {
		return fmt.Errorf("auth: chat id must not be zero")
	}
	def.mu.Lock()
	defer def.mu.Unlock()
	for _, permission := range permissions {
		delete(def.permissions[chatID], permission)
	}
	if len(def.permissions[chatID]) == 0 {
		delete(def.permissions, chatID)
	}
	return def.save()
}

func normalizedPermissions(values []Permission) ([]Permission, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("auth: at least one permission is required")
	}
	seen := make(map[Permission]struct{}, len(values))
	permissions := make([]Permission, 0, len(values))
	for _, value := range values {
		permission, ok := ParsePermission(value.String())
		if !ok {
			return nil, fmt.Errorf("auth: invalid permission %q", value)
		}
		if _, exists := seen[permission]; exists {
			continue
		}
		seen[permission] = struct{}{}
		permissions = append(permissions, permission)
	}
	sort.Slice(permissions, func(i, j int) bool { return permissions[i] < permissions[j] })
	return permissions, nil
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

// Member is a single authorization record.
type Member struct {
	ID          int64
	Role        Role
	Permissions []Permission
}

// Members returns every account with a role or explicit permission, sorted by
// role descending, then id ascending.
func Members() []Member {
	def.mu.RLock()
	defer def.mu.RUnlock()
	ids := make(map[int64]struct{}, len(def.roles)+len(def.permissions))
	for id := range def.roles {
		ids[id] = struct{}{}
	}
	for id := range def.permissions {
		ids[id] = struct{}{}
	}
	ms := make([]Member, 0, len(ids))
	for id := range ids {
		permissions := make([]Permission, 0, len(def.permissions[id]))
		for permission := range def.permissions[id] {
			permissions = append(permissions, permission)
		}
		sort.Slice(permissions, func(i, j int) bool { return permissions[i] < permissions[j] })
		ms = append(ms, Member{ID: id, Role: def.roles[id], Permissions: permissions})
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

func (s *store) authorizationCount() int {
	ids := make(map[int64]struct{}, len(s.roles)+len(s.permissions))
	for id := range s.roles {
		ids[id] = struct{}{}
	}
	for id := range s.permissions {
		ids[id] = struct{}{}
	}
	return len(ids)
}

func newCode() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- Persistence (JSON); load/save are called with the lock held and do no locking themselves ---

type entry struct {
	ID          int64        `json:"id"`
	Role        Role         `json:"role,omitempty"`
	Permissions []Permission `json:"permissions,omitempty"`
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
	seen := make(map[int64]struct{}, len(m.Members))
	for _, e := range m.Members {
		if e.ID == 0 {
			return fmt.Errorf("解析 %s: chat id 不能为 0", s.path)
		}
		if _, exists := seen[e.ID]; exists {
			return fmt.Errorf("解析 %s: chat id %d 重复", s.path, e.ID)
		}
		seen[e.ID] = struct{}{}
		if e.Role < RoleUser || e.Role > RoleOwner {
			return fmt.Errorf("解析 %s: chat id %d 的角色无效", s.path, e.ID)
		}
		if e.Role > RoleUser {
			s.roles[e.ID] = e.Role
		}
		permissions, permissionErr := normalizedPermissionsIfPresent(e.Permissions)
		if permissionErr != nil {
			return fmt.Errorf("解析 %s: chat id %d: %w", s.path, e.ID, permissionErr)
		}
		if len(permissions) > 0 {
			s.permissions[e.ID] = make(map[Permission]struct{}, len(permissions))
			for _, permission := range permissions {
				s.permissions[e.ID][permission] = struct{}{}
			}
		}
	}
	return nil
}

func normalizedPermissionsIfPresent(values []Permission) ([]Permission, error) {
	if len(values) == 0 {
		return nil, nil
	}
	return normalizedPermissions(values)
}

func (s *store) save() error {
	ids := make(map[int64]struct{}, len(s.roles)+len(s.permissions))
	for id := range s.roles {
		ids[id] = struct{}{}
	}
	for id := range s.permissions {
		ids[id] = struct{}{}
	}
	orderedIDs := make([]int64, 0, len(ids))
	for id := range ids {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Slice(orderedIDs, func(i, j int) bool { return orderedIDs[i] < orderedIDs[j] })

	m := fileModel{Members: make([]entry, 0, len(orderedIDs))}
	for _, id := range orderedIDs {
		permissions := make([]Permission, 0, len(s.permissions[id]))
		for permission := range s.permissions[id] {
			permissions = append(permissions, permission)
		}
		sort.Slice(permissions, func(i, j int) bool { return permissions[i] < permissions[j] })
		m.Members = append(m.Members, entry{ID: id, Role: s.roles[id], Permissions: permissions})
	}
	data, err := sonic.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".auth-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}
