// Command tracearr-connector is a Silo plugin that presents Silo to Tracearr
// as a read-only Jellyfin server.
package main

import (
	_ "embed"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

//go:embed manifest.json
var manifestJSON []byte

func main() {
	h := newHandler()
	runtime.ServeManifestWithOptions(
		manifestJSON,
		pluginVersion,
		runtime.CapabilityServers{HttpRoutes: h},
		runtime.WithConfigure(h.configure),
	)
}
