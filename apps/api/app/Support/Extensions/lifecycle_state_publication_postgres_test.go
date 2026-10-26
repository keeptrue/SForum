package extensionsruntime

import (
	"context"
	"errors"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestPostgresLifecycleBoundaryStateMapsDurableRepositoryTransaction(t *testing.T) {
	repository := &lifecycleStatePublicationRepositoryTestDouble{
		phase: extensions.LifecycleStatePublicationSource,
	}
	adapter := NewPostgresLifecycleBoundaryState(repository)
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineUpgrade, 8)

	transaction, err := adapter.PrepareLifecycleStatePublication(t.Context(), request, LifecycleBoundaryActivate)
	if err != nil {
		t.Fatal(err)
	}
	input := repository.input
	if input.OperationID != request.OperationID || input.Operation != request.Operation ||
		input.Position != request.Position || input.StepID != request.StepID || input.Attempt != request.Attempt ||
		input.Mode != extensions.LifecycleStatePublicationActivate || input.Source == nil ||
		input.Source.VersionID != request.SourceExtension.ActiveVersionID ||
		input.Target.VersionID != request.TargetExtension.ActiveVersionID {
		t.Fatalf("repository input = %#v", input)
	}
	state, err := transaction.Inspect(t.Context())
	if err != nil || state != LifecycleBoundaryTransactionSource {
		t.Fatalf("source inspection = %q, %v", state, err)
	}
	if err := transaction.Publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err = transaction.Inspect(t.Context())
	if err != nil || state != LifecycleBoundaryTransactionTarget {
		t.Fatalf("target inspection = %q, %v", state, err)
	}
	if err := transaction.Restore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if repository.publishCalls != 1 || repository.restoreCalls != 1 {
		t.Fatalf("publish=%d restore=%d", repository.publishCalls, repository.restoreCalls)
	}
}

func TestPostgresLifecycleBoundaryStateRejectsUnavailableOrUnknownState(t *testing.T) {
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 5)
	if _, err := NewPostgresLifecycleBoundaryState(nil).PrepareLifecycleStatePublication(
		t.Context(), request, LifecycleBoundaryActivate,
	); !errors.Is(err, ErrLifecycleBoundaryStateUnavailable) {
		t.Fatalf("nil repository error = %v", err)
	}
	repository := &lifecycleStatePublicationRepositoryTestDouble{phase: "unknown"}
	transaction, err := NewPostgresLifecycleBoundaryState(repository).PrepareLifecycleStatePublication(
		t.Context(), request, LifecycleBoundaryActivate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Inspect(t.Context()); !errors.Is(err, ErrLifecycleBoundaryInvalid) {
		t.Fatalf("unknown state error = %v", err)
	}
}

type lifecycleStatePublicationRepositoryTestDouble struct {
	input        extensions.PrepareLifecycleStatePublicationInput
	ref          extensions.LifecycleStatePublicationRef
	phase        extensions.LifecycleStatePublicationPhase
	publishCalls int
	restoreCalls int
}

func (r *lifecycleStatePublicationRepositoryTestDouble) PrepareLifecycleStatePublication(
	_ context.Context,
	input extensions.PrepareLifecycleStatePublicationInput,
) (extensions.LifecycleStatePublicationRef, error) {
	r.input = input
	r.ref = extensions.LifecycleStatePublicationRef{
		OperationID: input.OperationID, StepID: input.StepID, Mode: input.Mode, Attempt: input.Attempt,
	}
	return r.ref, nil
}

func (r *lifecycleStatePublicationRepositoryTestDouble) InspectLifecycleStatePublication(
	_ context.Context,
	ref extensions.LifecycleStatePublicationRef,
) (extensions.LifecycleStatePublicationPhase, error) {
	if ref != r.ref {
		return "", extensions.ErrLifecycleStatePublicationConflict
	}
	return r.phase, nil
}

func (r *lifecycleStatePublicationRepositoryTestDouble) PublishLifecycleState(
	_ context.Context,
	ref extensions.LifecycleStatePublicationRef,
) error {
	if ref != r.ref {
		return extensions.ErrLifecycleStatePublicationConflict
	}
	r.publishCalls++
	r.phase = extensions.LifecycleStatePublicationTarget
	return nil
}

func (r *lifecycleStatePublicationRepositoryTestDouble) RestoreLifecycleState(
	_ context.Context,
	ref extensions.LifecycleStatePublicationRef,
) error {
	if ref != r.ref {
		return extensions.ErrLifecycleStatePublicationConflict
	}
	r.restoreCalls++
	r.phase = extensions.LifecycleStatePublicationSource
	return nil
}

var _ extensions.LifecycleStatePublicationRepository = (*lifecycleStatePublicationRepositoryTestDouble)(nil)
