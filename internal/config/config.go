// Package config reads runtime configuration from environment variables.
package config

import "os"

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
