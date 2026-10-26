package main

import (
	"log"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func main() {
	server, err := newContentPolicyPluginV2()
	if err != nil {
		log.Fatalf("configure content-policy protocol v2: %v", err)
	}
	pluginv2.Serve(server)
}
