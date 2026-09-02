package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/moyoez/magibox/internal/config"
)

const (
	maxProviderResponse = 64 << 10
	maxProtocolToken    = 4096
)

type discoveryMetadata struct {
	Issuer                           string   `json:"issuer"`
	AuthorizationEndpoint            string   `json:"authorization_endpoint"`
	TokenEndpoint                    string   `json:"token_endpoint"`
	UserInfoEndpoint                 string   `json:"userinfo_endpoint"`
	JWKSURI                          string   `json:"jwks_uri"`
	IntrospectionEndpoint            string   `json:"introspection_endpoint"`
	RevocationEndpoint               string   `json:"revocation_endpoint"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	GrantTypesSupported              []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported    []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethods         []string `json:"token_endpoint_auth_methods_supported"`
	IntrospectionEndpointAuthMethods []string `json:"introspection_endpoint_auth_methods_supported"`
	RevocationEndpointAuthMethods    []string `json:"revocation_endpoint_auth_methods_supported"`
	IDTokenSigningAlgorithms         []string `json:"id_token_signing_alg_values_supported"`
	ScopesSupported                  []string `json:"scopes_supported"`
	ERPAccessVersion                 int      `json:"erp_access_version"`
}

type erpIdentityClient struct {
	issuer           string
	clientID         string
	clientSecret     string
	requestedScopes  []string
	oauth            oauth2.Config
	provider         *oidc.Provider
	verifier         *oidc.IDTokenVerifier
	httpClient       *http.Client
	introspectionURL string
	revocationURL    string
}

type identityClaims struct {
	Subject           string   `json:"sub"`
	Nonce             string   `json:"nonce"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Roles             []string `json:"roles"`
}

type erpAccessClaims struct {
	Version     int      `json:"version"`
	Allowed     bool     `json:"allowed"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

type introspectionClaims struct {
	Active    bool            `json:"active"`
	Subject   string          `json:"sub"`
	ClientID  string          `json:"client_id"`
	Audience  json.RawMessage `json:"aud"`
	Issuer    string          `json:"iss"`
	Scope     string          `json:"scope"`
	TokenType string          `json:"token_type"`
	ExpiresAt int64           `json:"exp"`
	IssuedAt  int64           `json:"iat"`
	AuthTime  int64           `json:"auth_time"`
	ERPAccess erpAccessClaims `json:"erp_access"`
}

func newERPIdentityClient(ctx context.Context, runtime config.OIDCConfig) (identityClient, error) {
	httpClient := oidcHTTPClient(ctx)
	providerContext := oidc.ClientContext(ctx, httpClient)
	provider, err := oidc.NewProvider(providerContext, runtime.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discover ERP provider: %w", err)
	}
	var metadata discoveryMetadata
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("oidc: decode ERP discovery metadata: %w", err)
	}
	if err := validateDiscovery(runtime, metadata); err != nil {
		return nil, err
	}
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInHeader
	return &erpIdentityClient{
		issuer:          runtime.Issuer,
		clientID:        runtime.ClientID,
		clientSecret:    runtime.ClientSecret,
		requestedScopes: append([]string(nil), runtime.Scopes...),
		oauth: oauth2.Config{
			ClientID: runtime.ClientID, ClientSecret: runtime.ClientSecret,
			RedirectURL: runtime.RedirectURL, Scopes: append([]string(nil), runtime.Scopes...),
			Endpoint: endpoint,
		},
		provider:         provider,
		verifier:         provider.Verifier(&oidc.Config{ClientID: runtime.ClientID, SupportedSigningAlgs: []string{oidc.RS256}}),
		httpClient:       httpClient,
		introspectionURL: metadata.IntrospectionEndpoint,
		revocationURL:    metadata.RevocationEndpoint,
	}, nil
}

func oidcHTTPClient(ctx context.Context) *http.Client {
	base := http.DefaultClient
	if configured, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && configured != nil {
		base = configured
	}
	client := *base
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if client.Timeout == 0 || client.Timeout > requestTimeout {
		client.Timeout = requestTimeout
	}
	return &client
}

func validateDiscovery(runtime config.OIDCConfig, metadata discoveryMetadata) error {
	if metadata.Issuer != runtime.Issuer {
		return fmt.Errorf("oidc: discovery issuer does not match configured issuer")
	}
	issuer, err := url.Parse(runtime.Issuer)
	if err != nil {
		return fmt.Errorf("oidc: parse configured issuer: %w", err)
	}
	for name, endpoint := range map[string]string{
		"authorization": metadata.AuthorizationEndpoint,
		"token":         metadata.TokenEndpoint,
		"userinfo":      metadata.UserInfoEndpoint,
		"jwks":          metadata.JWKSURI,
		"introspection": metadata.IntrospectionEndpoint,
		"revocation":    metadata.RevocationEndpoint,
	} {
		if err := validateProviderEndpoint(issuer, endpoint); err != nil {
			return fmt.Errorf("oidc: invalid %s endpoint: %w", name, err)
		}
	}
	requireDiscoveryValue := func(name, value string, supported []string) error {
		if !slices.Contains(supported, value) {
			return fmt.Errorf("oidc: discovery does not support %s %q", name, value)
		}
		return nil
	}
	checks := []struct {
		name      string
		value     string
		supported []string
	}{
		{"response type", "code", metadata.ResponseTypesSupported},
		{"grant type", "authorization_code", metadata.GrantTypesSupported},
		{"grant type", "refresh_token", metadata.GrantTypesSupported},
		{"PKCE method", "S256", metadata.CodeChallengeMethodsSupported},
		{"token authentication", "client_secret_basic", metadata.TokenEndpointAuthMethods},
		{"introspection authentication", "client_secret_basic", metadata.IntrospectionEndpointAuthMethods},
		{"revocation authentication", "client_secret_basic", metadata.RevocationEndpointAuthMethods},
		{"ID token signing algorithm", oidc.RS256, metadata.IDTokenSigningAlgorithms},
	}
	for _, check := range checks {
		if err := requireDiscoveryValue(check.name, check.value, check.supported); err != nil {
			return err
		}
	}
	for _, scope := range runtime.Scopes {
		if err := requireDiscoveryValue("scope", scope, metadata.ScopesSupported); err != nil {
			return err
		}
	}
	if metadata.ERPAccessVersion != 1 {
		return fmt.Errorf("oidc: ERP introspection access version 1 is required")
	}
	return nil
}

func validateProviderEndpoint(issuer *url.URL, raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("must be an exact HTTPS URL")
	}
	if !strings.EqualFold(endpoint.Scheme, issuer.Scheme) || !strings.EqualFold(endpoint.Host, issuer.Host) {
		return fmt.Errorf("must use the configured issuer origin")
	}
	return nil
}

func (c *erpIdentityClient) authorizationURL(state, nonce, verifier string) string {
	return c.oauth.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("response_mode", "query"),
		oauth2.S256ChallengeOption(verifier),
	)
}

func (c *erpIdentityClient) exchange(ctx context.Context, code, verifier, nonce string) (verifiedGrant, error) {
	if !validProtocolToken(code) || !validProtocolToken(verifier) || !validProtocolToken(nonce) {
		return verifiedGrant{}, fmt.Errorf("oidc: invalid authorization response")
	}
	ctx = oidc.ClientContext(ctx, c.httpClient)
	token, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return verifiedGrant{}, classifyTokenEndpointError(err, false)
	}
	return c.verifyGrant(ctx, token, nonce, "")
}

func (c *erpIdentityClient) refresh(ctx context.Context, refreshToken, expectedSubject string) (verifiedGrant, error) {
	if !validProtocolToken(refreshToken) || expectedSubject == "" {
		return verifiedGrant{}, fmt.Errorf("%w: invalid stored refresh credential", errProviderResponse)
	}
	ctx = oidc.ClientContext(ctx, c.httpClient)
	token, err := c.oauth.TokenSource(ctx, &oauth2.Token{
		RefreshToken: refreshToken,
		Expiry:       time.Unix(0, 0),
	}).Token()
	if err != nil {
		return verifiedGrant{}, classifyTokenEndpointError(err, true)
	}
	return c.verifyGrant(ctx, token, "", expectedSubject)
}

func (c *erpIdentityClient) revoke(ctx context.Context, refreshToken string) error {
	if !validProtocolToken(refreshToken) {
		return errCredentialRejected
	}
	form := url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}}
	req, err := c.formRequest(ctx, c.revocationURL, form)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("oidc: revoke ERP credential: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProviderResponse))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc: ERP revocation returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *erpIdentityClient) verifyGrant(ctx context.Context, token *oauth2.Token, expectedNonce, expectedSubject string) (verifiedGrant, error) {
	if token == nil || !validProtocolToken(token.AccessToken) ||
		!strings.EqualFold(strings.TrimSpace(token.TokenType), "Bearer") {
		return verifiedGrant{}, fmt.Errorf("%w: invalid access token response", errProviderResponse)
	}
	subject := expectedSubject
	rawIDToken, ok := token.Extra("id_token").(string)
	if ok && rawIDToken != "" {
		if !validProtocolToken(rawIDToken) {
			return verifiedGrant{}, fmt.Errorf("%w: invalid ID token encoding", errProviderResponse)
		}
		idToken, err := c.verifier.Verify(ctx, rawIDToken)
		if err != nil || idToken.Subject == "" {
			return verifiedGrant{}, fmt.Errorf("%w: invalid ID token", errProviderResponse)
		}
		// at_hash is optional for an ID Token returned from the token endpoint,
		// but it must validate whenever ERP includes it.
		if idToken.AccessTokenHash != "" {
			if err := idToken.VerifyAccessToken(token.AccessToken); err != nil {
				return verifiedGrant{}, fmt.Errorf("%w: invalid access-token hash", errProviderResponse)
			}
		}
		var idClaims identityClaims
		if err := idToken.Claims(&idClaims); err != nil || idClaims.Subject != idToken.Subject {
			return verifiedGrant{}, fmt.Errorf("%w: invalid ID token claims", errProviderResponse)
		}
		if expectedNonce != "" && !constantTimeStringEqual(idClaims.Nonce, expectedNonce) {
			return verifiedGrant{}, fmt.Errorf("%w: ID token nonce mismatch", errProviderResponse)
		}
		if expectedSubject != "" && !constantTimeStringEqual(idClaims.Subject, expectedSubject) {
			return verifiedGrant{}, fmt.Errorf("%w: subject changed", errProviderResponse)
		}
		subject = idClaims.Subject
	} else if expectedSubject == "" {
		return verifiedGrant{}, fmt.Errorf("%w: authorization response has no ID token", errProviderResponse)
	}
	userInfo, err := c.userInfo(ctx, token.AccessToken)
	if err != nil {
		return verifiedGrant{}, err
	}
	if userInfo.Subject == "" || !constantTimeStringEqual(userInfo.Subject, subject) {
		return verifiedGrant{}, fmt.Errorf("%w: UserInfo subject mismatch", errProviderResponse)
	}
	introspection, err := c.introspect(ctx, token.AccessToken)
	if err != nil {
		return verifiedGrant{}, err
	}
	if err := c.validateIntrospection(introspection, subject); err != nil {
		return verifiedGrant{}, err
	}
	if token.RefreshToken != "" && !validProtocolToken(token.RefreshToken) {
		return verifiedGrant{}, fmt.Errorf("%w: invalid refresh credential", errProviderResponse)
	}
	return verifiedGrant{
		Issuer:          c.issuer,
		Subject:         subject,
		DisplayName:     userInfo.Name,
		Username:        userInfo.PreferredUsername,
		Scopes:          strings.Fields(introspection.Scope),
		Roles:           introspection.ERPAccess.Roles,
		Permissions:     introspection.ERPAccess.Permissions,
		AccessExpiresAt: time.Unix(introspection.ExpiresAt, 0),
		RefreshToken:    token.RefreshToken,
	}, nil
}

func (c *erpIdentityClient) userInfo(ctx context.Context, accessToken string) (identityClaims, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.provider.UserInfoEndpoint(), nil)
	if err != nil {
		return identityClaims{}, fmt.Errorf("oidc: create UserInfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	var claims identityClaims
	if err := c.readJSON(req, &claims); err != nil {
		return identityClaims{}, fmt.Errorf("oidc: read ERP UserInfo: %w", err)
	}
	return claims, nil
}

func (c *erpIdentityClient) introspect(ctx context.Context, accessToken string) (introspectionClaims, error) {
	form := url.Values{"token": {accessToken}, "token_type_hint": {"access_token"}}
	req, err := c.formRequest(ctx, c.introspectionURL, form)
	if err != nil {
		return introspectionClaims{}, err
	}
	var claims introspectionClaims
	if err := c.readJSON(req, &claims); err != nil {
		return introspectionClaims{}, fmt.Errorf("oidc: introspect ERP access token: %w", err)
	}
	return claims, nil
}

func (c *erpIdentityClient) formRequest(ctx context.Context, endpoint string, form url.Values) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oidc: create provider request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(c.clientID), url.QueryEscape(c.clientSecret))
	return req, nil
}

func (c *erpIdentityClient) readJSON(req *http.Request, target any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponse+1))
	if err != nil {
		return err
	}
	if len(body) > maxProviderResponse {
		return fmt.Errorf("provider response is too large")
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return errCredentialRejected
	}
	if resp.StatusCode == http.StatusForbidden {
		return errAccessDenied
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return fmt.Errorf("provider returned a non-JSON response")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("provider returned trailing JSON data")
	}
	return nil
}

func (c *erpIdentityClient) validateIntrospection(claims introspectionClaims, subject string) error {
	if !claims.Active {
		return errCredentialRejected
	}
	if claims.Subject == "" || !constantTimeStringEqual(claims.Subject, subject) ||
		claims.ClientID != c.clientID || claims.Issuer != c.issuer ||
		!strings.EqualFold(claims.TokenType, "Bearer") || !audienceContains(claims.Audience, c.clientID) {
		return fmt.Errorf("%w: introspection identity mismatch", errProviderResponse)
	}
	expiresAt := time.Unix(claims.ExpiresAt, 0)
	now := time.Now()
	if !now.Before(expiresAt) || expiresAt.After(now.Add(15*time.Minute)) {
		return fmt.Errorf("%w: introspection expiry is invalid", errProviderResponse)
	}
	scopes := strings.Fields(claims.Scope)
	if len(scopes) == 0 || len(scopes) != len(normalizedStrings(scopes)) {
		return fmt.Errorf("%w: introspection scopes are invalid", errProviderResponse)
	}
	for _, required := range c.requestedScopes {
		if !slices.Contains(scopes, required) {
			return fmt.Errorf("%w: required scope is missing", errProviderResponse)
		}
	}
	if claims.ERPAccess.Version != 1 {
		return fmt.Errorf("%w: ERP access payload version is invalid", errProviderResponse)
	}
	if !claims.ERPAccess.Allowed {
		return errAccessDenied
	}
	if err := validateStringClaims(claims.ERPAccess.Roles, 256); err != nil {
		return fmt.Errorf("%w: invalid ERP roles", errProviderResponse)
	}
	if err := validateStringClaims(claims.ERPAccess.Permissions, 1000); err != nil {
		return fmt.Errorf("%w: invalid ERP permissions", errProviderResponse)
	}
	return nil
}

func validateStringClaims(values []string, maximum int) error {
	if len(values) > maximum {
		return fmt.Errorf("too many values")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("invalid value")
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate value")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func audienceContains(raw json.RawMessage, clientID string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == clientID
	}
	var multiple []string
	return json.Unmarshal(raw, &multiple) == nil && slices.Contains(multiple, clientID)
}

func validProtocolToken(value string) bool {
	if value == "" || len(value) > maxProtocolToken {
		return false
	}
	for index := range len(value) {
		if value[index] <= 0x20 || value[index] >= 0x7f {
			return false
		}
	}
	return true
}

func classifyTokenEndpointError(err error, refresh bool) error {
	var responseError *oauth2.RetrieveError
	if !errors.As(err, &responseError) {
		return fmt.Errorf("oidc: ERP token endpoint request failed: %w", err)
	}
	switch responseError.ErrorCode {
	case "access_denied":
		return errAccessDenied
	case "invalid_grant":
		if refresh {
			return errCredentialRejected
		}
		return fmt.Errorf("oidc: ERP rejected the authorization code")
	default:
		code := responseError.ErrorCode
		if code == "" {
			code = "http_error"
		}
		return fmt.Errorf("oidc: ERP token endpoint rejected the request (%s)", boundedText(code, 64))
	}
}
