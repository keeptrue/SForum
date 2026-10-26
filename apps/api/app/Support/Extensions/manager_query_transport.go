package extensionsruntime

import (
	"context"
	"errors"
	"fmt"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

type exactVersionedQueryInvoker interface {
	InvokeQueryInstance(
		context.Context,
		RuntimeInstanceIdentity,
		extensions.Extension,
		VersionedQueryRequest,
	) ([]queryregistry.QueryRow, error)
	FilterQueryResultInstance(
		context.Context,
		RuntimeInstanceIdentity,
		extensions.Extension,
		VersionedQueryResultFilterRequest,
	) ([]queryregistry.QueryRow, error)
}

// InvokeQueryInstance dispatches through the exact process protected by the
// caller's Manager admission lease. It never falls back to the active pointer.
func (m *RuntimeInvoker) InvokeQueryInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	request VersionedQueryRequest,
) ([]queryregistry.QueryRow, error) {
	invoker, err := m.exactVersionedQueryInvoker(ctx, identity)
	if err != nil {
		return nil, err
	}
	return invoker.InvokeQueryInstance(ctx, identity, extension, request)
}

// FilterQueryResultInstance has the same exact-instance boundary as the owner
// query call; cross-plugin filters therefore cannot drift to a replacement.
func (m *RuntimeInvoker) FilterQueryResultInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	request VersionedQueryResultFilterRequest,
) ([]queryregistry.QueryRow, error) {
	invoker, err := m.exactVersionedQueryInvoker(ctx, identity)
	if err != nil {
		return nil, err
	}
	return invoker.FilterQueryResultInstance(ctx, identity, extension, request)
}

func (m *managerCore) exactVersionedQueryInvoker(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
) (exactVersionedQueryInvoker, error) {
	if m == nil || ctx == nil {
		return nil, queryregistry.ErrProviderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := normalizeRuntimeInstanceIdentity(identity); err != nil {
		return nil, errors.Join(queryregistry.ErrArtifactUnavailable, err)
	}
	invoker, ok := m.starter.(exactVersionedQueryInvoker)
	if !ok {
		return nil, queryregistry.ErrProviderUnavailable
	}
	return invoker, nil
}

func (s *ProtocolStarter) InvokeQueryInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	request VersionedQueryRequest,
) ([]queryregistry.QueryRow, error) {
	client, err := s.exactQueryRuntimeClient(ctx, identity, extension)
	if err != nil {
		return nil, err
	}
	return client.InvokeQuery(ctx, request)
}

func (s *ProtocolStarter) FilterQueryResultInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	request VersionedQueryResultFilterRequest,
) ([]queryregistry.QueryRow, error) {
	client, err := s.exactQueryRuntimeClient(ctx, identity, extension)
	if err != nil {
		return nil, err
	}
	return client.FilterQueryResult(ctx, request)
}

func (s *ProtocolStarter) exactQueryRuntimeClient(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
) (*protocolV2Client, error) {
	if s == nil || ctx == nil {
		return nil, queryregistry.ErrProviderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return nil, errors.Join(queryregistry.ErrArtifactUnavailable, err)
	}
	var client *protocolV2Client
	if err := func() error {
		unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
		if err != nil {
			return err
		}
		defer unlock()
		instance := s.protocolInstance(identity)
		if instance == nil {
			return errors.Join(queryregistry.ErrArtifactUnavailable, protocolInstanceNotFound(identity))
		}
		if instance.protocolVersion != 2 {
			return queryregistry.ErrProviderUnavailable
		}
		var ok bool
		client, ok = instance.protocol.(*protocolV2Client)
		if !ok || client.identity == nil ||
			client.identity.GetExtensionId() != identity.ExtensionID ||
			client.identity.GetInstanceId() != identity.InstanceID ||
			client.identity.GetExtensionVersion() != instance.extensionVersion ||
			client.identity.GetArtifactDigest() != instance.artifactDigest ||
			extension.ID != identity.ExtensionID ||
			extension.Version != instance.extensionVersion ||
			extension.PackageDigest != instance.artifactDigest {
			return fmt.Errorf("%w: exact query runtime identity drift", queryregistry.ErrArtifactUnavailable)
		}
		s.mu.Lock()
		s.recordProtocolCallLocked(identity.ExtensionID)
		s.mu.Unlock()
		return nil
	}(); err != nil {
		return nil, err
	}
	return client, nil
}

var _ exactVersionedQueryInvoker = (*ProtocolStarter)(nil)

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) InvokeQueryInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	request VersionedQueryRequest,
) ([]queryregistry.QueryRow, error) {
	return m.invoker.InvokeQueryInstance(ctx, identity, extension, request)
}

func (m *Manager) FilterQueryResultInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	request VersionedQueryResultFilterRequest,
) ([]queryregistry.QueryRow, error) {
	return m.invoker.FilterQueryResultInstance(ctx, identity, extension, request)
}
