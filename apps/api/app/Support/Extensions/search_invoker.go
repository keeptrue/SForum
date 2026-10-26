package extensionsruntime

import (
	"context"
	"errors"
	"fmt"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// SearchEngineInvoker 由 ProtocolStarter 实现，供 Manager 做准入后转发。
type SearchEngineInvoker interface {
	SearchEngineProbe(context.Context, string, SearchEngineProbeRequest) (SearchEngineProbeResponse, error)
	SearchEngineEnsure(context.Context, string) (SearchEngineResult, error)
	SearchEngineIndex(context.Context, string, SearchEngineIndexRequest) (SearchEngineResult, error)
	SearchEngineDelete(context.Context, string, SearchEngineDeleteRequest) (SearchEngineResult, error)
	SearchEngineSearch(context.Context, string, SearchEngineSearchRequest) (SearchEngineSearchResponse, error)
}

func (m *managerCore) callSearchEngine(
	ctx context.Context,
	extensionID string,
	fn func(context.Context, SearchEngineInvoker) error,
) error {
	invoker, ok := m.starter.(SearchEngineInvoker)
	if !ok {
		return extensions.ErrRuntimeUnavailable
	}
	_, admission, err := m.host.AcquireActiveRuntimeCall(ctx, extensionID, RuntimeCallProvider)
	if err != nil {
		return errors.Join(extensions.ErrRuntimeUnavailable, err)
	}
	defer admission.Release()
	ctx = admission.Context

	release, rejected := m.resilience.tryEnter(ctx, extensionID)
	if rejected != "" {
		release(false, rejected)
		return fmt.Errorf("%s: %s", rejected, circuitMessage(rejected))
	}
	err = fn(ctx, invoker)
	release(err == nil, "extension.search_failed")
	return err
}

func (m *RuntimeInvoker) SearchEngineProbe(ctx context.Context, extensionID string, request SearchEngineProbeRequest) (SearchEngineProbeResponse, error) {
	var response SearchEngineProbeResponse
	err := m.callSearchEngine(ctx, extensionID, func(ctx context.Context, inv SearchEngineInvoker) error {
		var callErr error
		response, callErr = inv.SearchEngineProbe(ctx, extensionID, request)
		return callErr
	})
	return response, err
}

func (m *RuntimeInvoker) SearchEngineEnsure(ctx context.Context, extensionID string) (SearchEngineResult, error) {
	var response SearchEngineResult
	err := m.callSearchEngine(ctx, extensionID, func(ctx context.Context, inv SearchEngineInvoker) error {
		var callErr error
		response, callErr = inv.SearchEngineEnsure(ctx, extensionID)
		return callErr
	})
	return response, err
}

func (m *RuntimeInvoker) SearchEngineIndex(ctx context.Context, extensionID string, request SearchEngineIndexRequest) (SearchEngineResult, error) {
	var response SearchEngineResult
	err := m.callSearchEngine(ctx, extensionID, func(ctx context.Context, inv SearchEngineInvoker) error {
		var callErr error
		response, callErr = inv.SearchEngineIndex(ctx, extensionID, request)
		return callErr
	})
	return response, err
}

func (m *RuntimeInvoker) SearchEngineDelete(ctx context.Context, extensionID string, request SearchEngineDeleteRequest) (SearchEngineResult, error) {
	var response SearchEngineResult
	err := m.callSearchEngine(ctx, extensionID, func(ctx context.Context, inv SearchEngineInvoker) error {
		var callErr error
		response, callErr = inv.SearchEngineDelete(ctx, extensionID, request)
		return callErr
	})
	return response, err
}

func (m *RuntimeInvoker) SearchEngineSearch(ctx context.Context, extensionID string, request SearchEngineSearchRequest) (SearchEngineSearchResponse, error) {
	var response SearchEngineSearchResponse
	err := m.callSearchEngine(ctx, extensionID, func(ctx context.Context, inv SearchEngineInvoker) error {
		var callErr error
		response, callErr = inv.SearchEngineSearch(ctx, extensionID, request)
		return callErr
	})
	return response, err
}

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) SearchEngineProbe(ctx context.Context, extensionID string, request SearchEngineProbeRequest) (SearchEngineProbeResponse, error) {
	return m.invoker.SearchEngineProbe(ctx, extensionID, request)
}

func (m *Manager) SearchEngineEnsure(ctx context.Context, extensionID string) (SearchEngineResult, error) {
	return m.invoker.SearchEngineEnsure(ctx, extensionID)
}

func (m *Manager) SearchEngineIndex(ctx context.Context, extensionID string, request SearchEngineIndexRequest) (SearchEngineResult, error) {
	return m.invoker.SearchEngineIndex(ctx, extensionID, request)
}

func (m *Manager) SearchEngineDelete(ctx context.Context, extensionID string, request SearchEngineDeleteRequest) (SearchEngineResult, error) {
	return m.invoker.SearchEngineDelete(ctx, extensionID, request)
}

func (m *Manager) SearchEngineSearch(ctx context.Context, extensionID string, request SearchEngineSearchRequest) (SearchEngineSearchResponse, error) {
	return m.invoker.SearchEngineSearch(ctx, extensionID, request)
}
