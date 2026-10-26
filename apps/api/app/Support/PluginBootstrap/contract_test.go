package pluginbootstrap_test

import (
	"testing"

	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
)

func TestBootstrapABIV1IsIndependentFromApplicationProtocolV2(t *testing.T) {
	handshake := pluginbootstrap.HandshakeV1()
	if handshake.ProtocolVersion != 1 ||
		handshake.MagicCookieKey != "SFORUM_PLUGIN" ||
		handshake.MagicCookieValue != "sforum-plugin-v1" {
		t.Fatalf("bootstrap ABI v1 drifted: %#v", handshake)
	}
	if pluginbootstrap.ApplicationProtocolV2 != 2 ||
		pluginbootstrap.ApplicationProtocolV2Name != "sforum-plugin-v2" {
		t.Fatalf(
			"application Protocol V2 drifted: version=%d name=%q",
			pluginbootstrap.ApplicationProtocolV2,
			pluginbootstrap.ApplicationProtocolV2Name,
		)
	}
}

func TestSupportsProtocolV2RequiresExactApplicationProtocol(t *testing.T) {
	if !pluginbootstrap.SupportsProtocolV2("hashicorp-go-plugin", 2) {
		t.Fatal("valid Protocol V2 bootstrap contract was rejected")
	}
	for _, input := range []struct {
		rpc      string
		protocol int
	}{
		{rpc: "hashicorp-go-plugin", protocol: 1},
		{rpc: "", protocol: 2},
		{rpc: "other", protocol: 2},
		{rpc: "hashicorp-go-plugin", protocol: 3},
	} {
		if pluginbootstrap.SupportsProtocolV2(input.rpc, input.protocol) {
			t.Fatalf("unsupported bootstrap contract accepted: %#v", input)
		}
	}
}
