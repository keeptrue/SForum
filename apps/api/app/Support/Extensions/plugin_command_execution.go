package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func (m *Manager) SendMail(ctx context.Context, extensionID string, request MailProviderRequest) (MailProviderResponse, error) {
	return m.invoker.SendMail(ctx, extensionID, request)
}

func (m *Manager) ExecutePluginJob(ctx context.Context, invocation supportjobs.PluginJobInvocation) error {
	return m.invoker.ExecutePluginJob(ctx, invocation)
}

func (m *Manager) RefreshMailProvider(ctx context.Context, extensionID string) error {
	return m.invoker.RefreshMailProvider(ctx, extensionID)
}

var ErrPluginCommandRuntimeStale = errors.New("plugin command runtime contract is stale")

type PluginCommandExecutionResult struct {
	Contract PluginCommandContract
	Output   map[string]any
}

type pluginCommandRuntime interface {
	InvokePluginCommand(context.Context, RuntimeInstanceIdentity, PluginCommandContract, map[string]any) (map[string]any, error)
}

// ExecutePluginCommand resolves an immutable Registry contract, validates both
// typed documents, and holds exact runtime admission for the complete RPC.
func (m *RuntimeInvoker) ExecutePluginCommand(
	ctx context.Context,
	commandID string,
	input map[string]any,
	safeMode bool,
) (PluginCommandExecutionResult, error) {
	if m == nil || ctx == nil || m.hooks == nil || m.hooks.commands == nil {
		return PluginCommandExecutionResult{}, ErrPluginCommandRegistryInvalid
	}
	contract, err := m.hooks.commands.Resolve(commandID, safeMode)
	if err != nil {
		return PluginCommandExecutionResult{}, err
	}
	if err := m.hooks.commands.ValidateInput(contract, input); err != nil {
		return PluginCommandExecutionResult{}, err
	}
	runtime, ok := m.starter.(pluginCommandRuntime)
	if !ok {
		return PluginCommandExecutionResult{}, ErrProtocolInstanceUnsupported
	}
	identity := RuntimeInstanceIdentity{ExtensionID: contract.ExtensionID, InstanceID: contract.InstanceID}
	lease, err := m.AcquireRuntimeCall(ctx, identity, RuntimeCallCommand)
	if err != nil {
		return PluginCommandExecutionResult{}, err
	}
	defer lease.Release()
	clonedInput, err := cloneHookDocument(input)
	if err != nil {
		return PluginCommandExecutionResult{}, err
	}
	output, err := runtime.InvokePluginCommand(lease.Context, identity, contract, clonedInput)
	if err != nil {
		return PluginCommandExecutionResult{}, err
	}
	if err := m.hooks.commands.ValidateResult(contract, output); err != nil {
		return PluginCommandExecutionResult{}, err
	}
	clonedOutput, err := cloneHookDocument(output)
	if err != nil {
		return PluginCommandExecutionResult{}, err
	}
	return PluginCommandExecutionResult{Contract: contract, Output: clonedOutput}, nil
}

func validateFrozenPluginCommand(contract PluginCommandContract, declarations []extensions.ManifestCommand) error {
	for _, declaration := range declarations {
		if declaration.ID != contract.ID {
			continue
		}
		if declaration.ContractVersion != contract.ContractVersion || declaration.Handler != contract.Handler ||
			declaration.InputSchema != contract.InputSchema || declaration.ResultSchema != contract.ResultSchema ||
			declaration.Permission != contract.Permission || declaration.RecoverySafe != contract.RecoverySafe ||
			time.Duration(declaration.TimeoutMS)*time.Millisecond != contract.Timeout {
			return ErrPluginCommandRuntimeStale
		}
		return nil
	}
	return fmt.Errorf("%w: command %s", ErrPluginCommandRuntimeStale, strings.TrimSpace(contract.ID))
}

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) ExecutePluginCommand(
	ctx context.Context,
	commandID string,
	input map[string]any,
	safeMode bool,
) (PluginCommandExecutionResult, error) {
	return m.invoker.ExecutePluginCommand(ctx, commandID, input, safeMode)
}
