package account

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moyoez/magibox/internal/auth"
)

func testStore(t *testing.T, now time.Time) (*bindingStore, string) {
	t.Helper()
	dir := t.TempDir()
	if err := auth.Init(filepath.Join(dir, "auth.json")); err != nil {
		t.Fatal(err)
	}
	key := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	tokenCipher, err := newTokenCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "oidc.json")
	store, err := newBindingStore(path, "https://issuer.example/oidc", "client-1", tokenCipher, now)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestBindingStoreTicketsAndTransactionsAreSingleUse(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	ticket, err := store.issueTicket(telegramAccount{ID: 12345}, now)
	if err != nil {
		t.Fatal(err)
	}
	telegramID, err := store.consumeTicket(ticket, now)
	if err != nil || telegramID != 12345 {
		t.Fatalf("consume ticket = %d, %v", telegramID, err)
	}
	if _, err := store.consumeTicket(ticket, now); !errors.Is(err, errTicketInvalid) {
		t.Fatalf("replayed ticket error = %v", err)
	}
	state, err := store.beginTransaction(telegramID, "nonce", "verifier", now)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := store.consumeTransaction(state, now)
	if err != nil || transaction.TelegramID != telegramID || transaction.Nonce != "nonce" {
		t.Fatalf("consume transaction = %#v, %v", transaction, err)
	}
	if _, err := store.consumeTransaction(state, now); !errors.Is(err, errTransactionInvalid) {
		t.Fatalf("replayed state error = %v", err)
	}
}

func TestBindingStorePersistsEncryptedRefreshAndRestoresPermissions(t *testing.T) {
	now := time.Now().UTC()
	store, path := testStore(t, now)
	if err := auth.GrantPermissions(12345, "auth:manual"); err != nil {
		t.Fatal(err)
	}
	item := binding{
		TelegramID:      12345,
		Issuer:          store.issuer,
		Subject:         "pairwise-subject",
		DisplayName:     "测试账号",
		Scopes:          []string{"profile", "openid", "profile"},
		Roles:           []string{"operator"},
		Permissions:     []auth.Permission{"AUTH:AFF", "auth:aff"},
		AccessExpiresAt: now.Add(time.Hour).Unix(),
	}
	if err := store.putBinding(item, "refresh-secret", now); err != nil {
		t.Fatal(err)
	}
	if got := auth.Permissions(12345); !slices.Equal(got, []auth.Permission{"auth:aff", "auth:manual"}) {
		t.Fatalf("effective permissions = %v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "refresh-secret") {
		t.Fatal("binding store persisted plaintext refresh token")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("binding store mode = %o", info.Mode().Perm())
	}

	auth.ClearFederatedPermissions(12345)
	reloaded, err := newBindingStore(path, store.issuer, store.clientID, store.cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	if !auth.HasPermission(12345, "auth:aff") {
		t.Fatal("reloaded store did not restore the unexpired federated grant")
	}
	stored, ok := reloaded.binding(12345)
	if !ok || !slices.Equal(stored.Scopes, []string{"openid", "profile"}) {
		t.Fatalf("stored binding = %#v", stored)
	}
	refresh, err := reloaded.refreshToken(stored)
	if err != nil || refresh != "refresh-secret" {
		t.Fatalf("refresh = %q, %v", refresh, err)
	}
	_, _, removed, err := reloaded.remove(12345)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if auth.HasPermission(12345, "auth:aff") || !auth.HasPermission(12345, "auth:manual") {
		t.Fatal("unbind must clear only the federated source")
	}
}

func TestBindingStoreRejectsIdentityAndTelegramConflicts(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	base := binding{
		TelegramID: 1, Issuer: store.issuer, Subject: "subject-1",
		Permissions: []auth.Permission{"auth:aff"}, AccessExpiresAt: now.Add(time.Hour).Unix(),
	}
	if err := store.putBinding(base, "refresh-1", now); err != nil {
		t.Fatal(err)
	}
	otherTelegram := base
	otherTelegram.TelegramID = 2
	if err := store.putBinding(otherTelegram, "refresh-2", now); !errors.Is(err, errIdentityAlreadyBound) {
		t.Fatalf("identity conflict error = %v", err)
	}
	otherIdentity := base
	otherIdentity.Subject = "subject-2"
	if err := store.putBinding(otherIdentity, "refresh-3", now); !errors.Is(err, errTelegramAlreadyBound) {
		t.Fatalf("Telegram conflict error = %v", err)
	}
	invalidPermission := base
	invalidPermission.TelegramID = 3
	invalidPermission.Subject = "subject-3"
	invalidPermission.Permissions = []auth.Permission{"erp:admin"}
	if err := store.putBinding(invalidPermission, "refresh-4", now); err == nil {
		t.Fatal("non-auth ERP permission must fail closed")
	}
}

func TestBindingStoreRefreshesPermissionsOnFixedSchedule(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	item := binding{
		TelegramID: 34567, Issuer: store.issuer, Subject: "subject-periodic",
		Permissions: []auth.Permission{"auth:aff"}, AccessExpiresAt: now.Add(time.Hour).Unix(),
	}
	if err := store.putBinding(item, "refresh-periodic", now); err != nil {
		t.Fatal(err)
	}
	if due := store.due(now.Add(permissionSyncInterval-time.Second), refreshAhead); len(due) != 0 {
		t.Fatalf("bindings became due early: %#v", due)
	}
	if due := store.due(now.Add(permissionSyncInterval), refreshAhead); len(due) != 1 || due[0].TelegramID != item.TelegramID {
		t.Fatalf("periodic due bindings = %#v", due)
	}
	if err := store.recordRefreshFailure(item.TelegramID, true, now.Add(permissionSyncInterval)); err != nil {
		t.Fatal(err)
	}
	updated, ok := store.binding(item.TelegramID)
	if !ok || updated.EncryptedRefresh == "" || len(updated.Permissions) != 0 {
		t.Fatalf("failed refresh binding = %#v", updated)
	}
	if auth.HasPermission(item.TelegramID, "auth:aff") {
		t.Fatal("denied ERP permissions must be cleared while the binding remains")
	}
	if due := store.due(now.Add(permissionSyncInterval+time.Minute), refreshAhead); len(due) != 0 {
		t.Fatalf("failed binding retried before its next interval: %#v", due)
	}
	if due := store.due(now.Add(2*permissionSyncInterval), refreshAhead); len(due) != 1 {
		t.Fatalf("failed binding must remain scheduled: %#v", due)
	}
}

func TestBindingStoreLookupTicketKeepsDisplayName(t *testing.T) {
	now := time.Now().UTC()
	store, path := testStore(t, now)
	ticket, err := store.issueTicket(telegramAccount{ID: 12345, Name: "小林", Username: "alice01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.lookupTicket(ticket, now)
	if err != nil || account.ID != 12345 || account.Name != "小林" || account.Username != "alice01" {
		t.Fatalf("lookup = %#v, %v", account, err)
	}
	reloaded, err := newBindingStore(path, store.issuer, store.clientID, store.cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	account, err = reloaded.lookupTicket(ticket, now)
	if err != nil || account.Name != "小林" || account.Username != "alice01" {
		t.Fatalf("reloaded lookup = %#v, %v", account, err)
	}
}

func TestBindingStoreIssueTicketInvalidatesPreviousTicket(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	first, err := store.issueTicket(telegramAccount{ID: 12345}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.issueTicket(telegramAccount{ID: 12345}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.consumeTicket(first, now); !errors.Is(err, errTicketInvalid) {
		t.Fatalf("old ticket error = %v", err)
	}
	id, err := store.consumeTicket(second, now)
	if err != nil || id != 12345 {
		t.Fatalf("new ticket = %d, %v", id, err)
	}
}

func TestBindingStoreRestoresTicketWhenSaveFails(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	ticket, err := store.issueTicket(telegramAccount{ID: 12345}, now)
	if err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("disk full")
	if _, err := store.consumeTicket(ticket, now); err == nil {
		t.Fatal("consume should fail when the store cannot persist")
	}
	store.saveErr = nil
	id, err := store.consumeTicket(ticket, now)
	if err != nil || id != 12345 {
		t.Fatalf("ticket should be restored after a failed consume: %d, %v", id, err)
	}
}

func TestBindingStoreEncryptsTransactionSecrets(t *testing.T) {
	now := time.Now().UTC()
	store, path := testStore(t, now)
	state, err := store.beginTransaction(99, "nonce-secret", "verifier-secret", now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "nonce-secret") || strings.Contains(string(data), "verifier-secret") {
		t.Fatal("login transaction persisted plaintext nonce or PKCE verifier")
	}
	transaction, err := store.consumeTransaction(state, now)
	if err != nil || transaction.Nonce != "nonce-secret" || transaction.Verifier != "verifier-secret" {
		t.Fatalf("decrypted transaction = %#v, %v", transaction, err)
	}
}

func TestBindingStoreWALRecoversRotatedRefreshToken(t *testing.T) {
	now := time.Now().UTC()
	store, path := testStore(t, now)
	item := binding{
		TelegramID: 45678, Issuer: store.issuer, Subject: "subject-wal",
		Permissions: []auth.Permission{"auth:aff"}, AccessExpiresAt: now.Add(time.Hour).Unix(),
	}
	if err := store.putBinding(item, "refresh-old", now); err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("disk full")
	if err := store.putBinding(item, "refresh-new", now.Add(time.Minute)); err == nil {
		t.Fatal("putBinding should surface the store persist failure")
	}
	token, err := store.refreshToken(item)
	if err != nil || token != "refresh-new" {
		t.Fatalf("in-memory token after WAL = %q, %v", token, err)
	}
	store.saveErr = nil
	reloaded, err := newBindingStore(path, store.issuer, store.clientID, store.cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	token, err = reloaded.refreshToken(item)
	if err != nil || token != "refresh-new" {
		t.Fatalf("reloaded WAL token = %q, %v", token, err)
	}
	if _, err := os.Stat(path + ".wal"); !os.IsNotExist(err) {
		t.Fatalf("WAL file should be cleared after a successful reload: %v", err)
	}
}

func TestBindingStoreFallsBackToMainStoreWhenWALCannotBeWritten(t *testing.T) {
	now := time.Now().UTC()
	store, path := testStore(t, now)
	item := binding{
		TelegramID: 45680, Issuer: store.issuer, Subject: "subject-wal-fallback",
		Permissions: []auth.Permission{"auth:aff"}, AccessExpiresAt: now.Add(time.Hour).Unix(),
	}
	if err := store.putBinding(item, "refresh-old", now); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".wal", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.putBinding(item, "refresh-new", now.Add(time.Minute)); err != nil {
		t.Fatalf("main-store fallback failed: %v", err)
	}
	if err := os.Remove(path + ".wal"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newBindingStore(path, store.issuer, store.clientID, store.cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	token, err := reloaded.refreshToken(item)
	if err != nil || token != "refresh-new" {
		t.Fatalf("persisted fallback token = %q, %v", token, err)
	}
}

func TestBindingStoreUnbindClearsWALSoRestartDoesNotRestore(t *testing.T) {
	now := time.Now().UTC()
	store, path := testStore(t, now)
	item := binding{
		TelegramID: 45679, Issuer: store.issuer, Subject: "subject-unbind-wal",
		Permissions: []auth.Permission{"auth:aff"}, AccessExpiresAt: now.Add(time.Hour).Unix(),
	}
	if err := store.putBinding(item, "refresh-old", now); err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("disk full")
	if err := store.putBinding(item, "refresh-new", now.Add(time.Minute)); err == nil {
		t.Fatal("expected persist failure")
	}
	store.saveErr = nil
	if _, _, removed, err := store.remove(item.TelegramID); err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	reloaded, err := newBindingStore(path, store.issuer, store.clientID, store.cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.binding(item.TelegramID); ok {
		t.Fatal("WAL must not restore an unbound account")
	}
}
