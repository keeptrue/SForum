package main

import (
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func main() {
	cfg := LoadGitHubConfigFromEnv()
	oauth := NewGitHubOAuth(cfg, nil)
	registry, err := newGitHubIdentityRegistry(oauth)
	if err != nil {
		panic(err)
	}
	// 仅协商 identity.runtime@1；不声明其它 Host 权威能力。
	pluginv2.Serve(pluginv2.NewServer().
		WithFeatures(pluginv2.IdentityRuntimeProtocolFeature()).
		WithRuntimeStreams(pluginv2.RuntimeStreams{Lifecycle: handleLifecycle}).
		WithIdentityProviderRegistry(registry),
	)
}
