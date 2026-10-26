package main

import (
	"log"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func main() {
	registry, err := newWebPushProviderRegistry()
	if err != nil {
		log.Fatalf("configure Web Push provider: %v", err)
	}
	pluginv2.Serve(pluginv2.NewServer().WithProviderRegistry(registry))
}
