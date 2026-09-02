package account

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/config"
)

const (
	refreshAhead       = 3 * time.Minute
	requestTimeout     = 15 * time.Second
	refreshWorkerCount = 4
)

var (
	errAccessDenied       = errors.New("ERP access is denied")
	errCredentialRejected = errors.New("ERP credential is no longer valid")
	errProviderResponse   = errors.New("ERP OIDC response is invalid")
	errAccountNotBound    = errors.New("ERP account is not bound")
)

type userLock struct {
	mu   sync.Mutex
	refs int
}

type verifiedGrant struct {
	Issuer          string
	Subject         string
	DisplayName     string
	Username        string
	Scopes          []string
	Roles           []string
	Permissions     []string
	AccessExpiresAt time.Time
	RefreshToken    string
}

type identityClient interface {
	authorizationURL(state, nonce, verifier string) string
	exchange(ctx context.Context, code, verifier, nonce string) (verifiedGrant, error)
	refresh(ctx context.Context, refreshToken, expectedSubject string) (verifiedGrant, error)
	revoke(ctx context.Context, refreshToken string) error
}

type service struct {
	store    *bindingStore
	client   identityClient
	startURL string
	now      func() time.Time
	sweepMu  sync.Mutex
	locksMu  sync.Mutex
	locks    map[int64]*userLock
}

func newService(runtime config.OIDCConfig, client identityClient, tokenCipher *tokenCipher, now time.Time) (*service, error) {
	callback, err := url.Parse(runtime.RedirectURL)
	if err != nil {
		return nil, fmt.Errorf("oidc: parse callback URL: %w", err)
	}
	store, err := newBindingStore(runtime.StorePath, runtime.Issuer, runtime.ClientID, tokenCipher, now)
	if err != nil {
		return nil, err
	}
	return &service{
		store:  store,
		client: client,
		startURL: (&url.URL{
			Scheme: callback.Scheme,
			Host:   callback.Host,
			Path:   "/auth/oidc/start",
		}).String(),
		now:   time.Now,
		locks: make(map[int64]*userLock),
	}, nil
}

func (s *service) issueBindingURL(account telegramAccount) (string, error) {
	ticket, err := s.store.issueTicket(account, s.now())
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(s.startURL)
	if err != nil {
		return "", fmt.Errorf("oidc: parse start URL: %w", err)
	}
	query := parsed.Query()
	query.Set("ticket", ticket)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (s *service) previewTicket(ticket string) (telegramAccount, error) {
	return s.store.lookupTicket(ticket, s.now())
}

func (s *service) begin(ticket string) (string, string, error) {
	nonce, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	verifier, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	_, state, err := s.store.beginFromTicket(ticket, nonce, verifier, s.now())
	if err != nil {
		return "", "", err
	}
	return s.client.authorizationURL(state, nonce, verifier), state, nil
}

func (s *service) complete(ctx context.Context, state, code string) (binding, error) {
	transaction, err := s.store.consumeTransaction(state, s.now())
	if err != nil {
		return binding{}, err
	}
	unlock := s.lockUser(transaction.TelegramID)
	defer unlock()
	grant, err := s.client.exchange(ctx, code, transaction.Verifier, transaction.Nonce)
	if err != nil {
		return binding{}, err
	}
	stored, err := s.persistGrant(transaction.TelegramID, grant)
	if err != nil {
		return binding{}, s.revokeUnstoredGrant(ctx, transaction.TelegramID, grant, err)
	}
	return stored, nil
}

func (s *service) persistGrant(telegramID int64, grant verifiedGrant) (binding, error) {
	if grant.Issuer != s.store.issuer || grant.Subject == "" || grant.RefreshToken == "" {
		return binding{}, fmt.Errorf("oidc: provider returned an incomplete grant")
	}
	permissions := make([]auth.Permission, 0, len(grant.Permissions))
	for _, raw := range grant.Permissions {
		permission, ok := localPermissionFromERP(raw)
		if !ok {
			continue
		}
		permissions = append(permissions, permission)
	}
	expiresAt := grant.AccessExpiresAt.Unix()
	if grant.AccessExpiresAt.IsZero() {
		expiresAt = 0
	}
	item := binding{
		TelegramID:      telegramID,
		Issuer:          grant.Issuer,
		Subject:         grant.Subject,
		DisplayName:     boundedText(grant.DisplayName, 256),
		Username:        boundedText(grant.Username, 256),
		Scopes:          grant.Scopes,
		Roles:           grant.Roles,
		Permissions:     permissions,
		AccessExpiresAt: expiresAt,
	}
	if err := s.store.putBinding(item, grant.RefreshToken, s.now()); err != nil {
		return binding{}, err
	}
	stored, ok := s.store.binding(telegramID)
	if !ok {
		return binding{}, errAccountNotBound
	}
	return stored, nil
}

// localPermissionFromERP maps only Magi's magi:auth:* codes onto local auth:*
// capabilities. ERP roles, unprefixed permissions, other apps, and magi codes
// outside auth:* are ignored.
func localPermissionFromERP(value string) (auth.Permission, bool) {
	const magiAuth = "magi:auth:"
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(value, magiAuth) {
		return "", false
	}
	permission, ok := auth.ParsePermission(strings.TrimPrefix(value, "magi:"))
	if !ok || !strings.HasPrefix(permission.String(), "auth:") {
		return "", false
	}
	return permission, true
}

func (s *service) refreshDue() {
	if !s.sweepMu.TryLock() {
		return
	}
	defer s.sweepMu.Unlock()
	items := s.store.due(s.now(), refreshAhead)
	jobs := make(chan binding)
	var workers sync.WaitGroup
	for range min(refreshWorkerCount, len(items)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				unlock := s.lockUser(item.TelegramID)
				ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
				err := s.refreshOne(ctx, item)
				cancel()
				unlock()
				if err != nil {
					log.Printf("[oidc] 权限续期失败 telegram_user_id=%d: %v", item.TelegramID, err)
				}
			}
		}()
	}
	for _, item := range items {
		jobs <- item
	}
	close(jobs)
	workers.Wait()
}

// refreshNow immediately re-evaluates the bound account at ERP. It serializes
// with scheduled refreshes because ERP rotates refresh credentials on use.
func (s *service) refreshNow(ctx context.Context, telegramID int64) (binding, error) {
	unlock := s.lockUser(telegramID)
	defer unlock()
	item, ok := s.store.binding(telegramID)
	if !ok {
		return binding{}, errAccountNotBound
	}
	if err := s.refreshOne(ctx, item); err != nil {
		return binding{}, err
	}
	updated, ok := s.store.binding(telegramID)
	if !ok {
		return binding{}, errAccountNotBound
	}
	return updated, nil
}

func (s *service) refreshOne(ctx context.Context, item binding) error {
	current, ok := s.store.binding(item.TelegramID)
	if !ok || current.Issuer != item.Issuer || current.Subject != item.Subject {
		return errAccountNotBound
	}
	refreshToken, err := s.store.refreshToken(current)
	if err != nil {
		return err
	}
	grant, err := s.client.refresh(ctx, refreshToken, current.Subject)
	if err != nil {
		if _, stillBound := s.store.binding(item.TelegramID); !stillBound {
			return err
		}
		clearPermissions := errors.Is(err, errCredentialRejected) || errors.Is(err, errAccessDenied)
		if recordErr := s.store.recordRefreshFailure(item.TelegramID, clearPermissions, s.now()); recordErr != nil {
			if clearPermissions {
				return errors.Join(err, recordErr)
			}
			return recordErr
		}
		return err
	}
	latest, ok := s.store.binding(item.TelegramID)
	if !ok {
		return errAccountNotBound
	}
	if latest.Issuer != item.Issuer || latest.Subject != item.Subject {
		return errAccountNotBound
	}
	if grant.Issuer != latest.Issuer || grant.Subject != latest.Subject {
		return fmt.Errorf("oidc: provider returned a different subject")
	}
	if grant.RefreshToken == "" {
		grant.RefreshToken = refreshToken
	}
	_, err = s.persistGrant(item.TelegramID, grant)
	if err != nil {
		return s.revokeUnstoredGrant(ctx, item.TelegramID, grant, err)
	}
	return nil
}

func (s *service) unbind(ctx context.Context, telegramID int64) (bool, error) {
	unlock := s.lockUser(telegramID)
	defer unlock()
	_, refreshToken, removed, err := s.store.remove(telegramID)
	if err != nil || !removed || refreshToken == "" {
		return removed, err
	}
	if err := s.client.revoke(ctx, refreshToken); err != nil {
		return true, fmt.Errorf("oidc: local binding removed but provider revocation failed: %w", err)
	}
	return true, nil
}

func (s *service) lockUser(telegramID int64) func() {
	s.locksMu.Lock()
	if s.locks == nil {
		s.locks = make(map[int64]*userLock)
	}
	entry := s.locks[telegramID]
	if entry == nil {
		entry = &userLock{}
		s.locks[telegramID] = entry
	}
	entry.refs++
	s.locksMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.locksMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(s.locks, telegramID)
		}
		s.locksMu.Unlock()
	}
}

func (s *service) revokeUnstoredGrant(ctx context.Context, telegramID int64, grant verifiedGrant, persistErr error) error {
	if grant.RefreshToken == "" {
		return persistErr
	}
	if stored, ok := s.store.binding(telegramID); ok &&
		stored.Issuer == grant.Issuer && stored.Subject == grant.Subject {
		refreshToken, err := s.store.refreshToken(stored)
		if err == nil && constantTimeStringEqual(refreshToken, grant.RefreshToken) {
			return persistErr
		}
	}
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
	defer cancel()
	if err := s.client.revoke(revokeCtx, grant.RefreshToken); err != nil {
		return errors.Join(persistErr, fmt.Errorf("oidc: revoke unpersisted refresh credential: %w", err))
	}
	return persistErr
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if len(characters) > limit {
		characters = characters[:limit]
	}
	return string(characters)
}
