// Package config reads runtime configuration from environment variables.
package config

import (
	"os"
	"strings"
)

// OIDCConfig is the ERP OpenID Connect relying-party configuration. An empty
// issuer disables the integration; callers reject partially configured values.
type OIDCConfig struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	EncryptionKey string
	Scopes        []string
	StorePath     string
	RedirectURL   string
}

// PluginsMode returns the plugin filter mode from PLUGINS_MODE:
// "blacklist" (default; list = disabled) or "whitelist" (list = only enabled).
func PluginsMode() string {
	if m := os.Getenv("PLUGINS_MODE"); m != "" {
		return m
	}
	return "blacklist"
}

// PluginsList returns the comma-separated plugin names from PLUGINS_LIST
// used by the filter (nil when unset).
func PluginsList() []string {
	v := os.Getenv("PLUGINS_LIST")
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// Token returns the bot token issued by @BotFather (required).
func Token() string { return os.Getenv("BOT_TOKEN") }

// BotName is the bot's display name pushed via setMyName on startup
// (equivalent to BotFather's /setname). Empty = don't touch.
func BotName() string { return os.Getenv("BOT_NAME") }

// BotDescription is the long description shown on the empty-chat screen,
// pushed via setMyDescription (BotFather's /setdescription). Empty = don't touch.
func BotDescription() string { return os.Getenv("BOT_DESCRIPTION") }

// BotAbout is the short "about" text on the bot's profile, pushed via
// setMyShortDescription (BotFather's /setabouttext). Empty = don't touch.
func BotAbout() string { return os.Getenv("BOT_ABOUT") }

// AuthStorePath returns the persistence file path for role permissions (default auth.json).
func AuthStorePath() string {
	if p := os.Getenv("AUTH_STORE"); p != "" {
		return p
	}
	return "auth.json"
}

// PlaygroundStorePath returns the persistence file path for playground group config (default playground.json).
func PlaygroundStorePath() string {
	if p := os.Getenv("PLAYGROUND_STORE"); p != "" {
		return p
	}
	return "playground.json"
}

// VarsStorePath returns the persistence file path for the playground variable table (default vars.json).
func VarsStorePath() string {
	if p := os.Getenv("VARS_STORE"); p != "" {
		return p
	}
	return "vars.json"
}

// BundleStorePath returns the persistence file path for chat bundles (default bundles.json).
func BundleStorePath() string {
	if p := os.Getenv("BUNDLE_STORE"); p != "" {
		return p
	}
	return "bundles.json"
}

// BundleAddr returns the listen address of the bundle HTTP server (default :8099).
func BundleAddr() string {
	if a := os.Getenv("BUNDLE_ADDR"); a != "" {
		return a
	}
	return ":8099"
}

// BundleBaseURL returns the public URL prefix for bundle URLs (default http://localhost:8099).
func BundleBaseURL() string {
	if u := os.Getenv("BUNDLE_BASE_URL"); u != "" {
		return u
	}
	return "http://localhost:8099"
}

// BundleMediaDir returns the storage directory for bundle media files (default bundle-media).
func BundleMediaDir() string {
	if d := os.Getenv("BUNDLE_MEDIA_DIR"); d != "" {
		return d
	}
	return "bundle-media"
}

// PublicBaseURL returns the public URL prefix for plugin HTTP routes served on
// the shared server (e.g. inbound webhooks). It falls back to BUNDLE_BASE_URL
// since those routes live on the same server; set PUBLIC_BASE_URL only to
// override that (default same as BundleBaseURL).
func PublicBaseURL() string {
	if u := os.Getenv("PUBLIC_BASE_URL"); u != "" {
		return u
	}
	return BundleBaseURL()
}

// CloudflareStorePath returns the persistence file path for Cloudflare creds /
// workers / domain records (default cloudflare.json).
func CloudflareStorePath() string {
	if p := os.Getenv("CLOUDFLARE_STORE"); p != "" {
		return p
	}
	return "cloudflare.json"
}

// OIDC returns Magi's ERP OpenID Connect client configuration. The callback is
// intentionally derived from the trusted public base URL instead of request
// Host or forwarding headers.
func OIDC() OIDCConfig {
	scopes := strings.Fields(os.Getenv("MAGI_OIDC_SCOPES"))
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "offline_access"}
	}
	storePath := strings.TrimSpace(os.Getenv("MAGI_OIDC_STORE"))
	if storePath == "" {
		storePath = "oidc.json"
	}
	return OIDCConfig{
		Issuer:        strings.TrimSpace(os.Getenv("MAGI_OIDC_ISSUER")),
		ClientID:      strings.TrimSpace(os.Getenv("MAGI_OIDC_CLIENT_ID")),
		ClientSecret:  strings.TrimSpace(os.Getenv("MAGI_OIDC_CLIENT_SECRET")),
		EncryptionKey: strings.TrimSpace(os.Getenv("MAGI_OIDC_ENCRYPTION_KEY")),
		Scopes:        scopes,
		StorePath:     storePath,
		RedirectURL:   strings.TrimRight(PublicBaseURL(), "/") + "/auth/oidc/callback",
	}
}
