package account

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"

	"github.com/moyoez/magibox/internal/auth"
)

const (
	storeVersion           = 1
	bindTicketTTL          = 10 * time.Minute
	transactionTTL         = 10 * time.Minute
	permissionSyncInterval = 5 * time.Minute
)

var (
	errIdentityAlreadyBound = errors.New("oidc identity is already bound to another Telegram user")
	errTelegramAlreadyBound = errors.New("Telegram user is already bound to another OIDC identity")
	errTicketInvalid        = errors.New("bind ticket is invalid or expired")
	errTransactionInvalid   = errors.New("login transaction is invalid or expired")
)

type binding struct {
	TelegramID       int64             `json:"telegram_id"`
	Issuer           string            `json:"issuer"`
	Subject          string            `json:"subject"`
	DisplayName      string            `json:"display_name,omitempty"`
	Username         string            `json:"username,omitempty"`
	Scopes           []string          `json:"scopes,omitempty"`
	Roles            []string          `json:"roles,omitempty"`
	Permissions      []auth.Permission `json:"permissions,omitempty"`
	AccessExpiresAt  int64             `json:"access_expires_at"`
	NextRefreshAt    int64             `json:"next_refresh_at,omitempty"`
	EncryptedRefresh string            `json:"encrypted_refresh"`
	BoundAt          int64             `json:"bound_at"`
	UpdatedAt        int64             `json:"updated_at"`
}

type bindTicket struct {
	Digest     string `json:"digest"`
	TelegramID int64  `json:"telegram_id"`
	ExpiresAt  int64  `json:"expires_at"`
}

type loginTransaction struct {
	StateDigest       string `json:"state_digest"`
	TelegramID        int64  `json:"telegram_id"`
	Nonce             string `json:"nonce,omitempty"`
	Verifier          string `json:"verifier,omitempty"`
	EncryptedNonce    string `json:"encrypted_nonce,omitempty"`
	EncryptedVerifier string `json:"encrypted_verifier,omitempty"`
	ExpiresAt         int64  `json:"expires_at"`
}

type storeFile struct {
	Version      int                `json:"version"`
	Issuer       string             `json:"issuer"`
	ClientID     string             `json:"client_id"`
	Bindings     []binding          `json:"bindings,omitempty"`
	Tickets      []bindTicket       `json:"tickets,omitempty"`
	Transactions []loginTransaction `json:"transactions,omitempty"`
}

type walFile struct {
	Version  int     `json:"version"`
	Issuer   string  `json:"issuer"`
	ClientID string  `json:"client_id"`
	Binding  binding `json:"binding"`
}

type bindingStore struct {
	mu           sync.Mutex
	path         string
	issuer       string
	clientID     string
	cipher       *tokenCipher
	bindings     map[int64]binding
	tickets      map[string]bindTicket
	transactions map[string]loginTransaction
	saveErr      error
}

func newBindingStore(path, issuer, clientID string, tokenCipher *tokenCipher, now time.Time) (*bindingStore, error) {
	s := &bindingStore{
		path:         path,
		issuer:       issuer,
		clientID:     clientID,
		cipher:       tokenCipher,
		bindings:     make(map[int64]binding),
		tickets:      make(map[string]bindTicket),
		transactions: make(map[string]loginTransaction),
	}
	if err := s.load(now); err != nil {
		return nil, err
	}
	if err := s.applyWAL(); err != nil {
		return nil, err
	}
	for id, item := range s.bindings {
		if err := auth.ReplaceFederatedPermissions(
			id, time.Unix(item.AccessExpiresAt, 0), item.Permissions...,
		); err != nil {
			return nil, fmt.Errorf("oidc: restore Telegram user %d permissions: %w", id, err)
		}
	}
	return s, nil
}

func (s *bindingStore) issueTicket(telegramID int64, now time.Time) (string, error) {
	if telegramID <= 0 {
		return "", fmt.Errorf("oidc: Telegram user id must be positive")
	}
	raw, err := randomToken(32)
	if err != nil {
		return "", err
	}
	digest := tokenDigest(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	for existingDigest, ticket := range s.tickets {
		if ticket.TelegramID == telegramID {
			delete(s.tickets, existingDigest)
		}
	}
	s.tickets[digest] = bindTicket{
		Digest: digest, TelegramID: telegramID, ExpiresAt: now.Add(bindTicketTTL).Unix(),
	}
	if err := s.save(); err != nil {
		delete(s.tickets, digest)
		return "", err
	}
	return raw, nil
}

func (s *bindingStore) lookupTicket(raw string, now time.Time) (int64, error) {
	digest := tokenDigest(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	ticket, ok := s.tickets[digest]
	if !ok || ticket.ExpiresAt <= now.Unix() {
		return 0, errTicketInvalid
	}
	return ticket.TelegramID, nil
}

func (s *bindingStore) consumeTicket(raw string, now time.Time) (int64, error) {
	digest := tokenDigest(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	ticket, ok := s.tickets[digest]
	if !ok || ticket.ExpiresAt <= now.Unix() {
		return 0, errTicketInvalid
	}
	delete(s.tickets, digest)
	if err := s.save(); err != nil {
		s.tickets[digest] = ticket
		return 0, err
	}
	return ticket.TelegramID, nil
}

func (s *bindingStore) beginTransaction(telegramID int64, nonce, verifier string, now time.Time) (string, error) {
	state, err := randomToken(32)
	if err != nil {
		return "", err
	}
	digest := tokenDigest(state)
	sealed, err := s.sealTransactionSecrets(digest, nonce, verifier)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	sealed.TelegramID = telegramID
	sealed.ExpiresAt = now.Add(transactionTTL).Unix()
	s.transactions[digest] = sealed
	if err := s.save(); err != nil {
		delete(s.transactions, digest)
		return "", err
	}
	return state, nil
}

func (s *bindingStore) beginFromTicket(raw, nonce, verifier string, now time.Time) (int64, string, error) {
	state, err := randomToken(32)
	if err != nil {
		return 0, "", err
	}
	ticketDigest := tokenDigest(raw)
	stateDigest := tokenDigest(state)
	sealed, err := s.sealTransactionSecrets(stateDigest, nonce, verifier)
	if err != nil {
		return 0, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	ticket, ok := s.tickets[ticketDigest]
	if !ok || ticket.ExpiresAt <= now.Unix() {
		return 0, "", errTicketInvalid
	}
	sealed.TelegramID = ticket.TelegramID
	sealed.ExpiresAt = now.Add(transactionTTL).Unix()
	delete(s.tickets, ticketDigest)
	s.transactions[stateDigest] = sealed
	if err := s.save(); err != nil {
		s.tickets[ticketDigest] = ticket
		delete(s.transactions, stateDigest)
		return 0, "", err
	}
	return ticket.TelegramID, state, nil
}

func (s *bindingStore) consumeTransaction(state string, now time.Time) (loginTransaction, error) {
	digest := tokenDigest(state)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	transaction, ok := s.transactions[digest]
	if !ok || transaction.ExpiresAt <= now.Unix() {
		return loginTransaction{}, errTransactionInvalid
	}
	nonce, verifier, err := s.openTransactionSecrets(transaction)
	if err != nil {
		delete(s.transactions, digest)
		_ = s.save()
		return loginTransaction{}, err
	}
	delete(s.transactions, digest)
	if err := s.save(); err != nil {
		s.transactions[digest] = transaction
		return loginTransaction{}, err
	}
	transaction.Nonce = nonce
	transaction.Verifier = verifier
	return transaction, nil
}

func (s *bindingStore) putBinding(item binding, refreshToken string, now time.Time) error {
	if item.TelegramID <= 0 || item.Issuer != s.issuer || item.Subject == "" || refreshToken == "" {
		return fmt.Errorf("oidc: invalid verified binding")
	}
	permissions, err := normalizePermissions(item.Permissions)
	if err != nil {
		return err
	}
	item.Permissions = permissions
	item.Scopes = normalizedStrings(item.Scopes)
	item.Roles = normalizedStrings(item.Roles)

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, existing := range s.bindings {
		if existing.Issuer == item.Issuer && existing.Subject == item.Subject && id != item.TelegramID {
			return errIdentityAlreadyBound
		}
	}
	if existing, ok := s.bindings[item.TelegramID]; ok &&
		(existing.Issuer != item.Issuer || existing.Subject != item.Subject) {
		return errTelegramAlreadyBound
	}
	if existing, ok := s.bindings[item.TelegramID]; ok {
		item.BoundAt = existing.BoundAt
	}
	if item.BoundAt == 0 {
		item.BoundAt = now.Unix()
	}
	item.UpdatedAt = now.Unix()
	item.NextRefreshAt = now.Add(permissionSyncInterval).Unix()
	encrypted, err := s.cipher.seal(refreshToken, bindingAAD(item))
	if err != nil {
		return err
	}
	item.EncryptedRefresh = encrypted
	s.bindings[item.TelegramID] = item
	if err := s.writeWAL(item); err != nil {
		// ERP may already have rotated the refresh token. Prefer an atomic
		// main-store write over reverting memory to the now-dead credential.
		if saveErr := s.save(); saveErr != nil {
			authErr := auth.ReplaceFederatedPermissions(
				item.TelegramID, time.Unix(item.AccessExpiresAt, 0), item.Permissions...,
			)
			return errors.Join(
				fmt.Errorf("oidc: write binding wal: %w", err),
				fmt.Errorf("oidc: persist binding without wal: %w", saveErr),
				authErr,
			)
		}
		return auth.ReplaceFederatedPermissions(
			item.TelegramID, time.Unix(item.AccessExpiresAt, 0), item.Permissions...,
		)
	}
	if err := s.save(); err != nil {
		// WAL already has the new refresh credential. Keep memory aligned with
		// it so a later retry does not present the rotated-away token to ERP.
		return fmt.Errorf("oidc: binding write-ahead saved but store persist failed: %w", err)
	}
	if err := s.clearWAL(); err != nil {
		return err
	}
	return auth.ReplaceFederatedPermissions(
		item.TelegramID, time.Unix(item.AccessExpiresAt, 0), item.Permissions...,
	)
}

func (s *bindingStore) binding(telegramID int64) (binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.bindings[telegramID]
	return cloneBinding(item), ok
}

func (s *bindingStore) due(now time.Time, within time.Duration) []binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline := now.Add(within).Unix()
	nowUnix := now.Unix()
	items := make([]binding, 0, len(s.bindings))
	for _, item := range s.bindings {
		periodicDue := item.NextRefreshAt > 0 && item.NextRefreshAt <= nowUnix
		expiryDue := item.AccessExpiresAt > 0 && item.AccessExpiresAt <= deadline
		if periodicDue || expiryDue {
			items = append(items, cloneBinding(item))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].TelegramID < items[j].TelegramID })
	return items
}

func (s *bindingStore) refreshToken(item binding) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.bindings[item.TelegramID]
	if !ok || current.Issuer != item.Issuer || current.Subject != item.Subject {
		return "", errTransactionInvalid
	}
	return s.cipher.open(current.EncryptedRefresh, bindingAAD(current))
}

func (s *bindingStore) remove(telegramID int64) (binding, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.bindings[telegramID]
	if !ok {
		return binding{}, "", false, nil
	}
	refreshToken := ""
	if item.EncryptedRefresh != "" {
		var err error
		refreshToken, err = s.cipher.open(item.EncryptedRefresh, bindingAAD(item))
		if err != nil {
			return binding{}, "", false, err
		}
	}
	if err := s.clearWALMatching(telegramID); err != nil {
		return binding{}, "", false, err
	}
	delete(s.bindings, telegramID)
	if err := s.save(); err != nil {
		s.bindings[telegramID] = item
		return binding{}, "", false, err
	}
	auth.ClearFederatedPermissions(telegramID)
	return cloneBinding(item), refreshToken, true, nil
}

func (s *bindingStore) recordRefreshFailure(telegramID int64, clearPermissions bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.bindings[telegramID]
	if !ok {
		return nil
	}
	previous := item
	if clearPermissions {
		item.Permissions = nil
		item.AccessExpiresAt = 0
	}
	item.NextRefreshAt = now.Add(permissionSyncInterval).Unix()
	item.UpdatedAt = now.Unix()
	s.bindings[telegramID] = item
	if err := s.save(); err != nil {
		s.bindings[telegramID] = previous
		return err
	}
	if clearPermissions {
		auth.ClearFederatedPermissions(telegramID)
	}
	return nil
}

func (s *bindingStore) load(now time.Time) error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("oidc: read binding store: %w", err)
	}
	var model storeFile
	if err := sonic.Unmarshal(data, &model); err != nil {
		return fmt.Errorf("oidc: parse binding store: %w", err)
	}
	if model.Version != storeVersion || model.Issuer != s.issuer || model.ClientID != s.clientID {
		return fmt.Errorf("oidc: binding store does not match configured issuer and client")
	}
	subjects := make(map[string]int64, len(model.Bindings))
	for _, item := range model.Bindings {
		if item.TelegramID <= 0 || item.Issuer != s.issuer || item.Subject == "" {
			return fmt.Errorf("oidc: invalid persisted binding")
		}
		if _, exists := s.bindings[item.TelegramID]; exists {
			return fmt.Errorf("oidc: duplicate persisted Telegram binding")
		}
		subjectKey := item.Issuer + "\x00" + item.Subject
		if _, exists := subjects[subjectKey]; exists {
			return fmt.Errorf("oidc: duplicate persisted identity binding")
		}
		permissions, permissionErr := normalizePermissions(item.Permissions)
		if permissionErr != nil {
			return permissionErr
		}
		item.Permissions = permissions
		if item.EncryptedRefresh == "" {
			return fmt.Errorf("oidc: binding has no refresh credential")
		}
		if _, openErr := s.cipher.open(item.EncryptedRefresh, bindingAAD(item)); openErr != nil {
			return openErr
		}
		s.bindings[item.TelegramID] = cloneBinding(item)
		subjects[subjectKey] = item.TelegramID
	}
	for _, ticket := range model.Tickets {
		if ticket.TelegramID > 0 && ticket.ExpiresAt > now.Unix() && ticket.Digest != "" {
			s.tickets[ticket.Digest] = ticket
		}
	}
	for _, transaction := range model.Transactions {
		if transaction.TelegramID <= 0 || transaction.ExpiresAt <= now.Unix() || transaction.StateDigest == "" {
			continue
		}
		if transaction.EncryptedNonce == "" && transaction.Nonce == "" {
			continue
		}
		if transaction.EncryptedVerifier == "" && transaction.Verifier == "" {
			continue
		}
		s.transactions[transaction.StateDigest] = transaction
	}
	return nil
}

func (s *bindingStore) cleanup(now time.Time) {
	unix := now.Unix()
	for digest, ticket := range s.tickets {
		if ticket.ExpiresAt <= unix {
			delete(s.tickets, digest)
		}
	}
	for digest, transaction := range s.transactions {
		if transaction.ExpiresAt <= unix {
			delete(s.transactions, digest)
		}
	}
}

func (s *bindingStore) save() error {
	if s.saveErr != nil {
		return s.saveErr
	}
	model := storeFile{
		Version:      storeVersion,
		Issuer:       s.issuer,
		ClientID:     s.clientID,
		Bindings:     make([]binding, 0, len(s.bindings)),
		Tickets:      make([]bindTicket, 0, len(s.tickets)),
		Transactions: make([]loginTransaction, 0, len(s.transactions)),
	}
	for _, item := range s.bindings {
		model.Bindings = append(model.Bindings, cloneBinding(item))
	}
	for _, ticket := range s.tickets {
		model.Tickets = append(model.Tickets, ticket)
	}
	for _, transaction := range s.transactions {
		model.Transactions = append(model.Transactions, persistedTransaction(transaction))
	}
	sort.Slice(model.Bindings, func(i, j int) bool { return model.Bindings[i].TelegramID < model.Bindings[j].TelegramID })
	sort.Slice(model.Tickets, func(i, j int) bool { return model.Tickets[i].Digest < model.Tickets[j].Digest })
	sort.Slice(model.Transactions, func(i, j int) bool {
		return model.Transactions[i].StateDigest < model.Transactions[j].StateDigest
	})
	data, err := sonic.MarshalIndent(model, "", "  ")
	if err != nil {
		return fmt.Errorf("oidc: encode binding store: %w", err)
	}
	return writeAtomicFile(s.path, data)
}

func (s *bindingStore) walPath() string { return s.path + ".wal" }

func (s *bindingStore) writeWAL(item binding) error {
	model := walFile{
		Version:  storeVersion,
		Issuer:   s.issuer,
		ClientID: s.clientID,
		Binding:  cloneBinding(item),
	}
	data, err := sonic.MarshalIndent(model, "", "  ")
	if err != nil {
		return fmt.Errorf("oidc: encode binding wal: %w", err)
	}
	return writeAtomicFile(s.walPath(), data)
}

func (s *bindingStore) applyWAL() error {
	data, err := os.ReadFile(s.walPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("oidc: read binding wal: %w", err)
	}
	var model walFile
	if err := sonic.Unmarshal(data, &model); err != nil {
		return fmt.Errorf("oidc: parse binding wal: %w", err)
	}
	if model.Version != storeVersion || model.Issuer != s.issuer || model.ClientID != s.clientID {
		return fmt.Errorf("oidc: binding wal does not match configured issuer and client")
	}
	item := model.Binding
	if item.TelegramID <= 0 || item.Issuer != s.issuer || item.Subject == "" || item.EncryptedRefresh == "" {
		return fmt.Errorf("oidc: invalid persisted binding wal")
	}
	permissions, err := normalizePermissions(item.Permissions)
	if err != nil {
		return err
	}
	item.Permissions = permissions
	if _, err := s.cipher.open(item.EncryptedRefresh, bindingAAD(item)); err != nil {
		return err
	}
	s.bindings[item.TelegramID] = cloneBinding(item)
	subjects := make(map[string]int64, len(s.bindings))
	for id, existing := range s.bindings {
		key := existing.Issuer + "\x00" + existing.Subject
		if previous, exists := subjects[key]; exists && previous != id {
			return fmt.Errorf("oidc: duplicate persisted identity binding")
		}
		subjects[key] = id
	}
	if err := s.save(); err != nil {
		return err
	}
	return s.clearWAL()
}

func (s *bindingStore) clearWAL() error {
	if err := os.Remove(s.walPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("oidc: remove binding wal: %w", err)
	}
	return nil
}

func (s *bindingStore) clearWALMatching(telegramID int64) error {
	data, err := os.ReadFile(s.walPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("oidc: read binding wal: %w", err)
	}
	var model walFile
	if err := sonic.Unmarshal(data, &model); err != nil {
		return fmt.Errorf("oidc: parse binding wal: %w", err)
	}
	if model.Binding.TelegramID != telegramID {
		return nil
	}
	return s.clearWAL()
}

func writeAtomicFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".oidc-*.tmp")
	if err != nil {
		return fmt.Errorf("oidc: create binding store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("oidc: secure binding store: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("oidc: write binding store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("oidc: sync binding store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("oidc: close binding store: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("oidc: replace binding store: %w", err)
	}
	return nil
}

func (s *bindingStore) sealTransactionSecrets(stateDigest, nonce, verifier string) (loginTransaction, error) {
	aad := transactionAAD(stateDigest)
	encryptedNonce, err := s.cipher.seal(nonce, aad)
	if err != nil {
		return loginTransaction{}, err
	}
	encryptedVerifier, err := s.cipher.seal(verifier, aad)
	if err != nil {
		return loginTransaction{}, err
	}
	return loginTransaction{
		StateDigest:       stateDigest,
		EncryptedNonce:    encryptedNonce,
		EncryptedVerifier: encryptedVerifier,
	}, nil
}

func (s *bindingStore) openTransactionSecrets(transaction loginTransaction) (string, string, error) {
	aad := transactionAAD(transaction.StateDigest)
	nonce := transaction.Nonce
	if transaction.EncryptedNonce != "" {
		opened, err := s.cipher.open(transaction.EncryptedNonce, aad)
		if err != nil {
			return "", "", err
		}
		nonce = opened
	}
	verifier := transaction.Verifier
	if transaction.EncryptedVerifier != "" {
		opened, err := s.cipher.open(transaction.EncryptedVerifier, aad)
		if err != nil {
			return "", "", err
		}
		verifier = opened
	}
	if nonce == "" || verifier == "" {
		return "", "", fmt.Errorf("oidc: login transaction is missing secrets")
	}
	return nonce, verifier, nil
}

func persistedTransaction(transaction loginTransaction) loginTransaction {
	if transaction.EncryptedNonce != "" {
		transaction.Nonce = ""
	}
	if transaction.EncryptedVerifier != "" {
		transaction.Verifier = ""
	}
	return transaction
}

func transactionAAD(stateDigest string) string {
	return "oidc-txn\x00" + stateDigest
}

func normalizePermissions(values []auth.Permission) ([]auth.Permission, error) {
	if len(values) > 1000 {
		return nil, fmt.Errorf("oidc: too many permissions")
	}
	seen := make(map[auth.Permission]struct{}, len(values))
	permissions := make([]auth.Permission, 0, len(values))
	for _, value := range values {
		permission, ok := auth.ParsePermission(value.String())
		if !ok || !strings.HasPrefix(permission.String(), "auth:") {
			return nil, fmt.Errorf("oidc: invalid ERP permission %q", value)
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

func normalizedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func bindingAAD(item binding) string {
	return fmt.Sprintf("%s\x00%s\x00%d", item.Issuer, item.Subject, item.TelegramID)
}

func cloneBinding(item binding) binding {
	item.Scopes = append([]string(nil), item.Scopes...)
	item.Roles = append([]string(nil), item.Roles...)
	item.Permissions = append([]auth.Permission(nil), item.Permissions...)
	return item
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("oidc: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func tokenDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
