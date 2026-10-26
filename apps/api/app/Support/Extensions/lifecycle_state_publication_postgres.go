package extensionsruntime

import (
	"context"
	"errors"
	"fmt"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

var ErrLifecycleBoundaryStateUnavailable = errors.New("extension lifecycle state repository is unavailable")

// PostgresLifecycleBoundaryState adapts the Models exact-state repository to
// the composed boundary. Returned transactions retain only a durable row key;
// every method reloads source/target facts from PostgreSQL.
type PostgresLifecycleBoundaryState struct {
	repository extensions.LifecycleStatePublicationRepository
}

func NewPostgresLifecycleBoundaryState(
	repository extensions.LifecycleStatePublicationRepository,
) *PostgresLifecycleBoundaryState {
	return &PostgresLifecycleBoundaryState{repository: repository}
}

func (s *PostgresLifecycleBoundaryState) PrepareLifecycleStatePublication(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) (LifecycleBoundaryTransaction, error) {
	if s == nil || s.repository == nil || ctx == nil {
		return nil, ErrLifecycleBoundaryStateUnavailable
	}
	fence, err := lifecyclePublicationFenceFor(request, mode)
	if err != nil {
		return nil, err
	}
	target := extensions.LifecycleStatePublicationArtifact{
		ExtensionID: fence.Target.ExtensionID, Version: fence.Target.ExtensionVersion,
		PackageDigest: fence.Target.PackageDigest, VersionID: fence.Target.VersionID,
	}
	var source *extensions.LifecycleStatePublicationArtifact
	if fence.Source.Present {
		source = &extensions.LifecycleStatePublicationArtifact{
			ExtensionID: fence.Source.ExtensionID, Version: fence.Source.ExtensionVersion,
			PackageDigest: fence.Source.PackageDigest, VersionID: fence.Source.VersionID,
		}
	}
	ref, err := s.repository.PrepareLifecycleStatePublication(ctx, extensions.PrepareLifecycleStatePublicationInput{
		OperationID: fence.OperationID,
		Operation:   fence.Operation,
		Position:    fence.Position,
		StepID:      fence.StepID,
		Attempt:     fence.Attempt,
		Mode:        extensions.LifecycleStatePublicationMode(fence.Mode),
		Source:      source,
		Target:      target,
	})
	if err != nil {
		return nil, err
	}
	return &postgresLifecycleBoundaryStateTransaction{repository: s.repository, ref: ref}, nil
}

type postgresLifecycleBoundaryStateTransaction struct {
	repository extensions.LifecycleStatePublicationRepository
	ref        extensions.LifecycleStatePublicationRef
}

func (t *postgresLifecycleBoundaryStateTransaction) Inspect(
	ctx context.Context,
) (LifecycleBoundaryTransactionState, error) {
	if t == nil || t.repository == nil || ctx == nil {
		return "", ErrLifecycleBoundaryStateUnavailable
	}
	state, err := t.repository.InspectLifecycleStatePublication(ctx, t.ref)
	if err != nil {
		return "", err
	}
	switch state {
	case extensions.LifecycleStatePublicationSource:
		return LifecycleBoundaryTransactionSource, nil
	case extensions.LifecycleStatePublicationTarget:
		return LifecycleBoundaryTransactionTarget, nil
	default:
		return "", fmt.Errorf("%w: repository returned lifecycle state %q", ErrLifecycleBoundaryInvalid, state)
	}
}

func (t *postgresLifecycleBoundaryStateTransaction) Publish(ctx context.Context) error {
	if t == nil || t.repository == nil || ctx == nil {
		return ErrLifecycleBoundaryStateUnavailable
	}
	return t.repository.PublishLifecycleState(ctx, t.ref)
}

func (t *postgresLifecycleBoundaryStateTransaction) Restore(ctx context.Context) error {
	if t == nil || t.repository == nil || ctx == nil {
		return ErrLifecycleBoundaryStateUnavailable
	}
	return t.repository.RestoreLifecycleState(ctx, t.ref)
}

var _ LifecycleBoundaryState = (*PostgresLifecycleBoundaryState)(nil)
var _ LifecycleBoundaryTransaction = (*postgresLifecycleBoundaryStateTransaction)(nil)
