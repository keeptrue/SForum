package extensionruntime

import (
	"context"
	"errors"

	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

// StorageAdapterFactory is the narrow storage boundary consumed by product
// models. Bootstrap supplies an adapter backed by the legacy runtime.
type StorageAdapterFactory interface {
	NewStorageAdapter(extensionID string) (storage.Adapter, error)
	NewStorageInstanceAdapter(ctx context.Context, extensionID, instanceID string, settings map[string]string) (storage.Adapter, error)
	ProbeStorageInstance(ctx context.Context, extensionID string, settings map[string]string) error
	RemoveStorageInstance(ctx context.Context, extensionID, instanceID string) error
}

var (
	ErrStorageCircuitOpen = errors.New("extension.circuit_open")
	ErrStorageTimeout     = errors.New("extension.hook_timeout")
)

// StorageRPCError preserves the public failure reason without exposing the
// concrete runtime implementation to product models.
type StorageRPCError struct {
	Reason  string
	Message string
}

func (e *StorageRPCError) Error() string {
	if e == nil {
		return "storage rpc failed"
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Reason != "" {
		return e.Reason
	}
	return "storage rpc failed"
}
