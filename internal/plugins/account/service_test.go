package account

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/moyoez/magibox/internal/auth"
	"github.com/moyoez/magibox/internal/config"
)

type fakeIdentityClient struct {
	grant           verifiedGrant
	err             error
	refreshToken    string
	expectedSubject string
	exchangeGrant   verifiedGrant
	exchangeErr     error
	onRefresh       func(string)
	grantForSubject func(string) verifiedGrant
	revoked         []string
}

func (*fakeIdentityClient) authorizationURL(state, nonce, verifier string) string {
	return (&url.URL{Scheme: "https", Host: "issuer.example", Path: "/authorize", RawQuery: "state=" + url.QueryEscape(state)}).String()
}

func (c *fakeIdentityClient) exchange(context.Context, string, string, string) (verifiedGrant, error) {
	if c.exchangeErr != nil {
		return verifiedGrant{}, c.exchangeErr
	}
	if c.exchangeGrant.RefreshToken == "" && c.exchangeGrant.Subject == "" {
		return verifiedGrant{}, errors.New("not implemented")
	}
	return c.exchangeGrant, nil
}

func (c *fakeIdentityClient) refresh(_ context.Context, refreshToken, expectedSubject string) (verifiedGrant, error) {
	if c.onRefresh != nil {
		c.onRefresh(expectedSubject)
	}
	c.refreshToken = refreshToken
	c.expectedSubject = expectedSubject
	if c.grantForSubject != nil {
		return c.grantForSubject(expectedSubject), c.err
	}
	return c.grant, c.err
}

func (c *fakeIdentityClient) revoke(_ context.Context, refreshToken string) error {
	c.revoked = append(c.revoked, refreshToken)
	return nil
}

func TestLocalPermissionFromERP(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "Magi namespace", input: "magi:auth:aff", want: "auth:aff", ok: true},
		{name: "canonicalizes code", input: " MAGI:AUTH:REPORT ", want: "auth:report", ok: true},
		{name: "unprefixed local permission", input: "auth:aff", ok: false},
		{name: "ERP role-looking code", input: "project_owner", ok: false},
		{name: "other application", input: "other:auth:aff", ok: false},
		{name: "invalid local permission", input: "magi:aff", ok: false},
		{name: "empty local permission", input: "magi:", ok: false},
		{name: "other Magi namespace", input: "magi:bundle:write", ok: false},
		{name: "auth prefix trap", input: "magi:authentication:admin", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			permission, ok := localPermissionFromERP(test.input)
			if ok != test.ok || permission.String() != test.want {
				t.Fatalf("localPermissionFromERP(%q) = %q, %v", test.input, permission, ok)
			}
		})
	}
}

func TestValidateOIDCConfig(t *testing.T) {
	valid := config.OIDCConfig{
		Issuer: "https://duke.qdzxbaogao.com/api/oidc", ClientID: "client",
		ClientSecret: "secret", EncryptionKey: "key", Scopes: []string{"openid", "profile", "offline_access"},
		StorePath: "oidc.json", RedirectURL: "https://playground-magi.aiacg.vip/auth/oidc/callback",
	}
	if err := validateOIDCConfig(valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*config.OIDCConfig)
	}{
		{name: "missing client", mutate: func(value *config.OIDCConfig) { value.ClientID = "" }},
		{name: "insecure issuer", mutate: func(value *config.OIDCConfig) { value.Issuer = "http://issuer.example" }},
		{name: "host-derived callback", mutate: func(value *config.OIDCConfig) { value.RedirectURL = "https://example.com/other" }},
		{name: "missing openid", mutate: func(value *config.OIDCConfig) { value.Scopes = []string{"offline_access"} }},
		{name: "missing refresh", mutate: func(value *config.OIDCConfig) { value.Scopes = []string{"openid"} }},
		{name: "duplicate scope", mutate: func(value *config.OIDCConfig) { value.Scopes = []string{"openid", "openid", "offline_access"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			value.Scopes = append([]string(nil), valid.Scopes...)
			test.mutate(&value)
			if err := validateOIDCConfig(value); err == nil {
				t.Fatal("configuration should fail")
			}
		})
	}
}

func TestServiceRefreshNowAtomicallyReplacesERPPermissions(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	if err := auth.GrantPermissions(12345, "auth:manual"); err != nil {
		t.Fatal(err)
	}
	initial := binding{
		TelegramID: 12345, Issuer: store.issuer, Subject: "subject-1",
		Permissions: []auth.Permission{"auth:old"}, AccessExpiresAt: now.Add(5 * time.Minute).Unix(),
	}
	if err := store.putBinding(initial, "refresh-old", now); err != nil {
		t.Fatal(err)
	}
	client := &fakeIdentityClient{grant: verifiedGrant{
		Issuer: store.issuer, Subject: initial.Subject, Permissions: []string{"magi:auth:new"},
		AccessExpiresAt: now.Add(10 * time.Minute), RefreshToken: "refresh-new",
	}}
	service := &service{store: store, client: client, now: func() time.Time { return now }}
	updated, err := service.refreshNow(context.Background(), initial.TelegramID)
	if err != nil {
		t.Fatal(err)
	}
	if client.refreshToken != "refresh-old" || client.expectedSubject != initial.Subject {
		t.Fatalf("refresh input = token %q subject %q", client.refreshToken, client.expectedSubject)
	}
	if len(updated.Permissions) != 1 || updated.Permissions[0] != "auth:new" {
		t.Fatalf("updated permissions = %v", updated.Permissions)
	}
	if auth.HasPermission(initial.TelegramID, "auth:old") || !auth.HasPermission(initial.TelegramID, "auth:new") ||
		!auth.HasPermission(initial.TelegramID, "auth:manual") {
		t.Fatal("refresh must replace only the ERP permission source")
	}
	storedRefresh, err := store.refreshToken(updated)
	if err != nil || storedRefresh != "refresh-new" {
		t.Fatalf("rotated refresh token = %q, %v", storedRefresh, err)
	}
}

func TestServiceRefreshNowClearsERPPermissionsWhenAccessIsRevoked(t *testing.T) {
	now := time.Now().UTC()
	dir := t.TempDir()
	if err := auth.Init(filepath.Join(dir, "auth.json")); err != nil {
		t.Fatal(err)
	}
	key := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	tokenCipher, err := newTokenCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newBindingStore(filepath.Join(dir, "oidc.json"), "https://issuer.example/oidc", "client-1", tokenCipher, now)
	if err != nil {
		t.Fatal(err)
	}
	const telegramID int64 = 23456
	if err := auth.GrantPermissions(telegramID, "auth:manual"); err != nil {
		t.Fatal(err)
	}
	initial := binding{
		TelegramID: telegramID, Issuer: store.issuer, Subject: "subject-2",
		Permissions: []auth.Permission{"auth:aff"}, AccessExpiresAt: now.Add(5 * time.Minute).Unix(),
	}
	if err := store.putBinding(initial, "refresh-token", now); err != nil {
		t.Fatal(err)
	}
	service := &service{
		store: store, client: &fakeIdentityClient{err: errAccessDenied}, now: func() time.Time { return now },
	}
	if _, err := service.refreshNow(context.Background(), telegramID); !errors.Is(err, errAccessDenied) {
		t.Fatalf("refresh error = %v", err)
	}
	updated, ok := store.binding(telegramID)
	if !ok || updated.EncryptedRefresh == "" || updated.NextRefreshAt <= now.Unix() {
		t.Fatalf("revoked binding = %#v", updated)
	}
	if auth.HasPermission(telegramID, "auth:aff") || !auth.HasPermission(telegramID, "auth:manual") {
		t.Fatal("provider revocation must clear only ERP permissions")
	}
	if _, err := service.refreshNow(context.Background(), 99999); !errors.Is(err, errAccountNotBound) {
		t.Fatalf("missing binding error = %v", err)
	}
}

func TestServiceRefreshProtocolFailurePreservesPermissionsUntilExpiry(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	const telegramID int64 = 23457
	initial := binding{
		TelegramID: telegramID, Issuer: store.issuer, Subject: "subject-protocol",
		Permissions: []auth.Permission{"auth:aff"}, AccessExpiresAt: now.Add(5 * time.Minute).Unix(),
	}
	if err := store.putBinding(initial, "refresh-token", now); err != nil {
		t.Fatal(err)
	}
	service := &service{
		store:  store,
		client: &fakeIdentityClient{err: fmt.Errorf("%w: malformed ERP payload", errProviderResponse)},
		now:    func() time.Time { return now },
	}
	if _, err := service.refreshNow(context.Background(), telegramID); !errors.Is(err, errProviderResponse) {
		t.Fatalf("refresh error = %v", err)
	}
	updated, ok := store.binding(telegramID)
	if !ok || len(updated.Permissions) != 1 || updated.AccessExpiresAt != initial.AccessExpiresAt {
		t.Fatalf("binding after protocol failure = %#v", updated)
	}
	if !auth.HasPermission(telegramID, "auth:aff") {
		t.Fatal("transient protocol failure must not be treated as ERP revocation")
	}
}

func TestServiceRevokesRefreshCredentialThatCouldNotBePersisted(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	client := &fakeIdentityClient{}
	service := &service{store: store, client: client, now: func() time.Time { return now }}
	persistErr := errors.New("identity conflict")
	grant := verifiedGrant{
		Issuer: store.issuer, Subject: "subject-unused", RefreshToken: "refresh-unused",
	}
	if err := service.revokeUnstoredGrant(context.Background(), 76543, grant, persistErr); !errors.Is(err, persistErr) {
		t.Fatalf("persist error = %v", err)
	}
	if len(client.revoked) != 1 || client.revoked[0] != grant.RefreshToken {
		t.Fatalf("revoked credentials = %v", client.revoked)
	}
}

func TestServiceRefreshIgnoresNonAuthERPPermissions(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	initial := binding{
		TelegramID: 34501, Issuer: store.issuer, Subject: "subject-mixed",
		Permissions: []auth.Permission{"auth:old"}, AccessExpiresAt: now.Add(5 * time.Minute).Unix(),
	}
	if err := store.putBinding(initial, "refresh-old", now); err != nil {
		t.Fatal(err)
	}
	client := &fakeIdentityClient{grant: verifiedGrant{
		Issuer: store.issuer, Subject: initial.Subject,
		Permissions:     []string{"project_owner", "magi:auth:aff", "magi:bundle:write", "auth:report"},
		AccessExpiresAt: now.Add(10 * time.Minute), RefreshToken: "refresh-new",
	}}
	service := &service{store: store, client: client, now: func() time.Time { return now }}
	updated, err := service.refreshNow(context.Background(), initial.TelegramID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Permissions) != 1 || updated.Permissions[0] != "auth:aff" {
		t.Fatalf("mapped permissions = %v", updated.Permissions)
	}
}

func TestServiceUnbindWinsOverInFlightRefresh(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	const telegramID int64 = 34502
	initial := binding{
		TelegramID: telegramID, Issuer: store.issuer, Subject: "subject-race",
		Permissions: []auth.Permission{"auth:aff"}, AccessExpiresAt: now.Add(5 * time.Minute).Unix(),
	}
	if err := store.putBinding(initial, "refresh-old", now); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	client := &fakeIdentityClient{
		grant: verifiedGrant{
			Issuer: store.issuer, Subject: initial.Subject, Permissions: []string{"magi:auth:new"},
			AccessExpiresAt: now.Add(10 * time.Minute), RefreshToken: "refresh-new",
		},
		onRefresh: func(string) {
			close(started)
			<-release
		},
	}
	service := &service{store: store, client: client, now: func() time.Time { return now }}
	refreshErr := make(chan error, 1)
	go func() {
		_, err := service.refreshNow(context.Background(), telegramID)
		refreshErr <- err
	}()
	<-started
	unbindErr := make(chan error, 1)
	go func() {
		_, err := service.unbind(context.Background(), telegramID)
		unbindErr <- err
	}()
	close(release)
	if err := <-refreshErr; err != nil && !errors.Is(err, errAccountNotBound) {
		t.Fatalf("refresh error = %v", err)
	}
	if err := <-unbindErr; err != nil {
		t.Fatalf("unbind error = %v", err)
	}
	if _, ok := store.binding(telegramID); ok {
		t.Fatal("in-flight refresh must not recreate an unbound account")
	}
}

func TestServiceUnrelatedAccountsDoNotBlockEachOtherDuringRefresh(t *testing.T) {
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	for id, subject := range map[int64]string{34503: "subject-slow", 34504: "subject-fast"} {
		item := binding{
			TelegramID: id, Issuer: store.issuer, Subject: subject,
			Permissions: []auth.Permission{"auth:aff"}, AccessExpiresAt: now.Add(5 * time.Minute).Unix(),
		}
		if err := store.putBinding(item, "refresh-"+subject, now); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{})
	release := make(chan struct{})
	client := &fakeIdentityClient{
		grantForSubject: func(subject string) verifiedGrant {
			return verifiedGrant{
				Issuer: store.issuer, Subject: subject, Permissions: []string{"magi:auth:aff"},
				AccessExpiresAt: now.Add(10 * time.Minute), RefreshToken: "refresh-new-" + subject,
			}
		},
		onRefresh: func(subject string) {
			if subject == "subject-slow" {
				close(started)
				<-release
			}
		},
	}
	service := &service{store: store, client: client, now: func() time.Time { return now }}
	slowDone := make(chan error, 1)
	go func() {
		_, err := service.refreshNow(context.Background(), 34503)
		slowDone <- err
	}()
	<-started

	fastDone := make(chan error, 1)
	go func() {
		_, err := service.refreshNow(context.Background(), 34504)
		fastDone <- err
	}()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated account refresh was blocked by a slow provider request")
	}
	close(release)
	if err := <-slowDone; err != nil {
		t.Fatal(err)
	}
}
