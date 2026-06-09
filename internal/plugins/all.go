// Package plugins aggregates all plugins via blank imports, triggering each
// one's init() self-registration. To add a plugin, add one blank import here;
// main needs no changes.
package plugins

import (
	_ "github.com/moyoez/magibox/internal/plugins/bind"
	_ "github.com/moyoez/magibox/internal/plugins/bundle"
	_ "github.com/moyoez/magibox/internal/plugins/echo"
	_ "github.com/moyoez/magibox/internal/plugins/heartbeat"
	_ "github.com/moyoez/magibox/internal/plugins/perm"
	_ "github.com/moyoez/magibox/internal/plugins/ping"
	_ "github.com/moyoez/magibox/internal/plugins/playground"
)
