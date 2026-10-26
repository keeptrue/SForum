package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"

	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	installationidentity "github.com/keeptrue/sforum/apps/api/app/Support/InstallationIdentity"
	pluginsessionstate "github.com/keeptrue/sforum/apps/api/app/Support/PluginSessionState"
)

// productionPluginSessionState is the process-wide S1-S9 substrate: one store,
// one signing authority, one Protocol V2 service, plus the narrow invalidators
// the three erasure consumers (AuthSession, Identity, Extensions) declare on
// their own side.
//
// The store is built early (it only needs the shared Redis client and the
// durable installation id) because the Extensions service must receive its
// erase hook at construction; the gateway binding happens later, once the Host
// API gateway and the runtime admission source exist.
type productionPluginSessionState struct {
	Store     *pluginsessionstate.Store
	Authority *hostapi.ProtocolV2SessionDelegationAuthority
	Service   *hostapi.ProtocolV2SessionServiceServer

	installationID string
}

func newProductionPluginSessionState(
	installationID string,
	client *redis.Client,
) (*productionPluginSessionState, error) {
	if client == nil {
		return nil, fmt.Errorf("bootstrap: plugin session state requires the shared Redis client")
	}
	if !installationidentity.Valid(installationID) {
		return nil, fmt.Errorf("bootstrap: plugin session state installation identity is invalid")
	}
	backend, err := pluginsessionstate.NewRedisBackend(client)
	if err != nil {
		return nil, fmt.Errorf("create plugin session state backend: %w", err)
	}
	store, err := pluginsessionstate.NewStore(backend)
	if err != nil {
		return nil, fmt.Errorf("create plugin session state store: %w", err)
	}
	authority, err := hostapi.NewProtocolV2SessionDelegationAuthority()
	if err != nil {
		return nil, fmt.Errorf("create plugin session delegation authority: %w", err)
	}
	return &productionPluginSessionState{
		Store: store, Authority: authority, installationID: installationID,
	}, nil
}

// BindGateway installs the S9 session-state service on the Host API gateway.
// Safe Mode is decided once, here, so a quarantined extension can never observe
// a window in which session state is readable.
func (p *productionPluginSessionState) BindGateway(
	safeMode bool,
	admission hostapi.ServiceProviderAdmission,
	gateway *hostapi.Gateway,
) error {
	if p == nil || p.Store == nil || p.Authority == nil || gateway == nil || admission == nil {
		return fmt.Errorf("bootstrap: plugin session state gateway binding requires the store, authority, admission and gateway")
	}
	service, err := hostapi.NewProtocolV2SessionServiceServer(
		p.Store, p.Authority, p.installationID,
		hostapi.WithProtocolV2SessionSafeMode(safeMode),
		hostapi.WithProtocolV2SessionAdmission(admission),
	)
	if err != nil {
		return fmt.Errorf("create Protocol V2 session service: %w", err)
	}
	if err := gateway.BindProtocolV2SessionService(service); err != nil {
		return fmt.Errorf("bind Protocol V2 session service: %w", err)
	}
	p.Service = service
	return nil
}

// InvalidateSessionBySID satisfies the consumer-side ports declared by
// Support/AuthSession and Models/Identity. Fingerprinting the raw sid happens
// here and only here, so the derivation exists in exactly one place outside the
// store: those two packages never learn how a session maps to a key.
func (p *productionPluginSessionState) InvalidateSessionBySID(ctx context.Context, sid string) (int, error) {
	if p == nil || p.Store == nil {
		return 0, pluginsessionstate.ErrInvalid
	}
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return 0, nil
	}
	return p.Store.InvalidateSession(ctx, p.installationID, pluginsessionstate.Fingerprint(sid))
}

// InvalidateExtension satisfies Models/Extensions' erase port.
func (p *productionPluginSessionState) InvalidateExtension(ctx context.Context, extensionID string) (int, error) {
	if p == nil || p.Store == nil {
		return 0, pluginsessionstate.ErrInvalid
	}
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return 0, nil
	}
	return p.Store.InvalidateExtension(ctx, p.installationID, extensionID)
}
