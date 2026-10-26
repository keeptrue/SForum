package main

import (
	"log"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func main() {
	plugin, err := newAdminSurfaceReferencePlugin()
	if err != nil {
		log.Fatalf("configure admin surface reference plugin: %v", err)
	}
	pluginv2.Serve(plugin)
}
