package bootstrap

import (
	"fmt"

	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

type protocolV2ProviderBrokerSource interface {
	ProtocolV2ProviderBroker() (hostapi.ProtocolV2ProviderBroker, error)
}

func bindProtocolV2ProviderBroker(gateway *hostapi.Gateway, source protocolV2ProviderBrokerSource) error {
	if gateway == nil || source == nil {
		return fmt.Errorf("Protocol V2 provider broker source is required")
	}
	broker, err := source.ProtocolV2ProviderBroker()
	if err != nil {
		return fmt.Errorf("create Protocol V2 provider broker: %w", err)
	}
	if err := gateway.BindProtocolV2ProviderBroker(broker); err != nil {
		return fmt.Errorf("bind Protocol V2 provider broker: %w", err)
	}
	return nil
}
