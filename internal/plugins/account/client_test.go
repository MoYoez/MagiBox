package account

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/moyoez/magibox/internal/config"
)

type oidcProviderFixture struct {
	t                  *testing.T
	server             *httptest.Server
	key                *rsa.PrivateKey
	issuer             string
	nonce              string
	active             bool
	allowed            bool
	revoked            bool
	mu                 sync.Mutex
	lastVerifier       string
	lastGrantType      string
	omitRefreshIDToken bool
}

func newOIDCProviderFixture(t *testing.T) *oidcProviderFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &oidcProviderFixture{t: t, key: key, nonce: "nonce-value", active: true, allowed: true}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.serveHTTP))
	fixture.issuer = fixture.server.URL + "/oidc"
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *oidcProviderFixture) runtime() config.OIDCConfig {
	return config.OIDCConfig{
		Issuer: f.issuer, ClientID: "client-id", ClientSecret: "client-secret",
		RedirectURL: "https://magi.example/auth/oidc/callback",
		Scopes:      []string{"openid", "profile", "offline_access"},
	}
}

func (f *oidcProviderFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/oidc/.well-known/openid-configuration":
		f.writeJSON(w, map[string]any{
			"issuer": f.issuer, "authorization_endpoint": f.issuer + "/authorize/",
			"token_endpoint": f.issuer + "/token/", "userinfo_endpoint": f.issuer + "/userinfo/",
			"jwks_uri": f.issuer + "/jwks/", "introspection_endpoint": f.issuer + "/introspect/",
			"revocation_endpoint": f.issuer + "/revoke/", "response_types_supported": []string{"code"},
			"grant_types_supported":                         []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":              []string{"S256"},
			"token_endpoint_auth_methods_supported":         []string{"client_secret_basic"},
			"introspection_endpoint_auth_methods_supported": []string{"client_secret_basic"},
			"revocation_endpoint_auth_methods_supported":    []string{"client_secret_basic"},
			"id_token_signing_alg_values_supported":         []string{"RS256"},
			"scopes_supported":                              []string{"openid", "profile", "offline_access"},
			"erp_access_version":                            1,
		})
	case "/oidc/jwks/":
		f.writeJSON(w, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "test-key",
			"n": base64.RawURLEncoding.EncodeToString(f.key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.PublicKey.E)).Bytes()),
		}}})
	case "/oidc/token/":
		f.requireClientAuth(r)
		if err := r.ParseForm(); err != nil {
			f.t.Errorf("parse token form: %v", err)
		}
		grantType := r.Form.Get("grant_type")
		f.mu.Lock()
		f.lastGrantType = grantType
		f.lastVerifier = r.Form.Get("code_verifier")
		f.mu.Unlock()
		if grantType == "authorization_code" && (r.Form.Get("code") != "code-value" || r.Form.Get("redirect_uri") != "https://magi.example/auth/oidc/callback") {
			w.WriteHeader(http.StatusBadRequest)
			f.writeJSON(w, map[string]any{"error": "invalid_grant"})
			return
		}
		if grantType == "refresh_token" && r.Form.Get("refresh_token") != "refresh-token" {
			w.WriteHeader(http.StatusBadRequest)
			f.writeJSON(w, map[string]any{"error": "invalid_grant"})
			return
		}
		response := map[string]any{
			"access_token": "access-token", "refresh_token": "refresh-token",
			"token_type": "Bearer", "expires_in": 600,
		}
		f.mu.Lock()
		omitIDToken := grantType == "refresh_token" && f.omitRefreshIDToken
		f.mu.Unlock()
		if !omitIDToken {
			response["id_token"] = f.idToken()
		}
		f.writeJSON(w, response)
	case "/oidc/userinfo/":
		if r.Header.Get("Authorization") != "Bearer access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			f.writeJSON(w, map[string]any{"error": "invalid_token"})
			return
		}
		f.writeJSON(w, map[string]any{
			"sub": "subject-1", "name": "ERP User", "preferred_username": "erp-user", "roles": []string{"erp-user"},
		})
	case "/oidc/introspect/":
		f.requireClientAuth(r)
		f.mu.Lock()
		active, allowed := f.active, f.allowed
		f.mu.Unlock()
		f.writeJSON(w, map[string]any{
			"active": active, "sub": "subject-1", "client_id": "client-id", "aud": "client-id",
			"iss": f.issuer, "scope": "openid profile offline_access", "token_type": "Bearer",
			"exp": time.Now().Add(10 * time.Minute).Unix(), "iat": time.Now().Unix(), "auth_time": time.Now().Unix(),
			"erp_access": map[string]any{
				"version": 1, "mode": "identity_only", "allowed": allowed,
				"project_id": nil, "department_id": nil, "evaluated_at": time.Now().Unix(),
				"roles":       []string{"affiliate"},
				"permissions": []string{"magi:auth:aff", "other:ignored"},
			},
		})
	case "/oidc/revoke/":
		f.requireClientAuth(r)
		f.mu.Lock()
		f.revoked = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (f *oidcProviderFixture) requireClientAuth(r *http.Request) {
	f.t.Helper()
	clientID, secret, ok := r.BasicAuth()
	if !ok || clientID != "client-id" || secret != "client-secret" {
		f.t.Errorf("client authentication = %q, %q, %v", clientID, secret, ok)
	}
}

func (f *oidcProviderFixture) writeJSON(w http.ResponseWriter, value any) {
	f.t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		f.t.Errorf("encode provider response: %v", err)
	}
}

func (f *oidcProviderFixture) idToken() string {
	f.t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": f.issuer, "sub": "subject-1", "aud": "client-id",
		"exp": time.Now().Add(10 * time.Minute).Unix(), "iat": time.Now().Unix(),
		"auth_time": time.Now().Unix(), "nonce": f.nonce,
		"name": "ERP User", "preferred_username": "erp-user",
	})
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (f *oidcProviderFixture) client(t *testing.T) *erpIdentityClient {
	t.Helper()
	ctx := oidc.ClientContext(context.Background(), f.server.Client())
	client, err := newERPIdentityClient(ctx, f.runtime())
	if err != nil {
		t.Fatal(err)
	}
	return client.(*erpIdentityClient)
}

func TestERPIdentityClientValidatesCompleteAuthorizationAndRefresh(t *testing.T) {
	fixture := newOIDCProviderFixture(t)
	client := fixture.client(t)
	authorizationURL := client.authorizationURL("state-value", fixture.nonce, "verifier-value-which-is-at-least-forty-three-characters")
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("state") != "state-value" || query.Get("nonce") != fixture.nonce || query.Get("code_challenge_method") != "S256" ||
		query.Get("code_challenge") == "" || strings.Contains(query.Get("code_challenge"), "verifier-value") {
		t.Fatalf("authorization query = %v", query)
	}
	grant, err := client.exchange(context.Background(), "code-value", "verifier-value-which-is-at-least-forty-three-characters", fixture.nonce)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Issuer != fixture.issuer || grant.Subject != "subject-1" || grant.DisplayName != "ERP User" ||
		grant.Username != "erp-user" || grant.RefreshToken != "refresh-token" ||
		len(grant.Permissions) != 2 || grant.Permissions[0] != "magi:auth:aff" {
		t.Fatalf("exchange grant = %#v", grant)
	}
	refreshed, err := client.refresh(context.Background(), "refresh-token", "subject-1")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Subject != grant.Subject || refreshed.AccessExpiresAt.Before(time.Now()) {
		t.Fatalf("refresh grant = %#v", refreshed)
	}
	if err := client.revoke(context.Background(), "refresh-token"); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !fixture.revoked || fixture.lastGrantType != "refresh_token" {
		t.Fatalf("provider state = revoked %v grant %q", fixture.revoked, fixture.lastGrantType)
	}
}

func TestERPIdentityClientAcceptsRefreshWithoutOptionalIDToken(t *testing.T) {
	fixture := newOIDCProviderFixture(t)
	fixture.mu.Lock()
	fixture.omitRefreshIDToken = true
	fixture.mu.Unlock()
	client := fixture.client(t)
	grant, err := client.refresh(context.Background(), "refresh-token", "subject-1")
	if err != nil {
		t.Fatal(err)
	}
	if grant.Subject != "subject-1" || grant.RefreshToken != "refresh-token" {
		t.Fatalf("refresh grant = %#v", grant)
	}
}

func TestERPIdentityClientRejectsNonceRevocationAndDeniedAccess(t *testing.T) {
	fixture := newOIDCProviderFixture(t)
	client := fixture.client(t)
	if _, err := client.exchange(context.Background(), "code-value", "verifier-value-which-is-at-least-forty-three-characters", "wrong-nonce"); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("nonce mismatch error = %v", err)
	}
	fixture.mu.Lock()
	fixture.active = false
	fixture.mu.Unlock()
	if _, err := client.refresh(context.Background(), "refresh-token", "subject-1"); !errors.Is(err, errCredentialRejected) {
		t.Fatalf("inactive token error = %v", err)
	}
	fixture.mu.Lock()
	fixture.active, fixture.allowed = true, false
	fixture.mu.Unlock()
	if _, err := client.refresh(context.Background(), "refresh-token", "subject-1"); !errors.Is(err, errAccessDenied) {
		t.Fatalf("denied access error = %v", err)
	}
}

func TestValidateDiscoveryRejectsCrossOriginProtocolEndpoint(t *testing.T) {
	fixture := newOIDCProviderFixture(t)
	runtime := fixture.runtime()
	metadata := discoveryMetadata{
		Issuer: runtime.Issuer, AuthorizationEndpoint: runtime.Issuer + "/authorize/",
		TokenEndpoint: "https://attacker.example/token", UserInfoEndpoint: runtime.Issuer + "/userinfo/",
		JWKSURI: runtime.Issuer + "/jwks/", IntrospectionEndpoint: runtime.Issuer + "/introspect/",
		RevocationEndpoint: runtime.Issuer + "/revoke/", ResponseTypesSupported: []string{"code"},
		GrantTypesSupported: []string{"authorization_code", "refresh_token"}, CodeChallengeMethodsSupported: []string{"S256"},
		TokenEndpointAuthMethods: []string{"client_secret_basic"}, IntrospectionEndpointAuthMethods: []string{"client_secret_basic"},
		RevocationEndpointAuthMethods: []string{"client_secret_basic"}, IDTokenSigningAlgorithms: []string{"RS256"},
		ScopesSupported: runtime.Scopes, ERPAccessVersion: 1,
	}
	if err := validateDiscovery(runtime, metadata); err == nil || !strings.Contains(err.Error(), "issuer origin") {
		t.Fatalf("cross-origin discovery error = %v", err)
	}
}
