package main

import pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"

func main() {
	pluginv2.Serve(newSiteSearchPluginV2())
}
