package config

import (
	"slices"
	"testing"
)

func TestOIDCDefaultsAndTrustedCallback(t *testing.T) {
	t.Setenv("MAGI_OIDC_ISSUER", " https://duke.qdzxbaogao.com/api/oidc ")
	t.Setenv("MAGI_OIDC_CLIENT_ID", " client-id ")
	t.Setenv("MAGI_OIDC_CLIENT_SECRET", " secret ")
	t.Setenv("MAGI_OIDC_ENCRYPTION_KEY", " key ")
	t.Setenv("MAGI_OIDC_SCOPES", "")
	t.Setenv("MAGI_OIDC_STORE", "")
	t.Setenv("PUBLIC_BASE_URL", "https://playground-magi.aiacg.vip/")

	got := OIDC()
	if got.Issuer != "https://duke.qdzxbaogao.com/api/oidc" || got.ClientID != "client-id" {
		t.Fatalf("trimmed config = %#v", got)
	}
	if !slices.Equal(got.Scopes, []string{"openid", "profile", "offline_access"}) {
		t.Fatalf("default scopes = %v", got.Scopes)
	}
	if got.StorePath != "oidc.json" {
		t.Fatalf("store = %q", got.StorePath)
	}
	if got.RedirectURL != "https://playground-magi.aiacg.vip/auth/oidc/callback" {
		t.Fatalf("redirect = %q", got.RedirectURL)
	}
}

func TestOIDCCustomScopes(t *testing.T) {
	t.Setenv("MAGI_OIDC_SCOPES", " openid   profile erp:roles offline_access ")
	if got := OIDC().Scopes; !slices.Equal(got, []string{"openid", "profile", "erp:roles", "offline_access"}) {
		t.Fatalf("scopes = %v", got)
	}
}
