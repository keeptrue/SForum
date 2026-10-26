package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

type fakePluginCommandConsole struct {
	commands []pluginCommandDescriptor
	result   pluginCommandRunResult
	err      error
	input    map[string]any
	closed   bool
}

func (f *fakePluginCommandConsole) List(context.Context) ([]pluginCommandDescriptor, error) {
	return f.commands, f.err
}

func (f *fakePluginCommandConsole) Run(_ context.Context, _ string, input map[string]any) (pluginCommandRunResult, error) {
	f.input = input
	return f.result, f.err
}

func (f *fakePluginCommandConsole) Close(context.Context) { f.closed = true }

func TestPluginCommandCLIListsValidatedNamespace(t *testing.T) {
	fake := &fakePluginCommandConsole{commands: []pluginCommandDescriptor{{
		ID: "demo.commands.command.sync", ExtensionID: "demo.commands", ExtensionVersion: "1.0.0",
		InputSchema: "demo.commands.command.input@1", RecoverySafe: true, Available: true,
	}}}
	withPluginCommandConsole(t, fake)
	root := newRootCommand()
	root.SetArgs([]string{"extension", "command", "list", "--json"})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"id": "demo.commands.command.sync"`) ||
		!strings.Contains(output.String(), `"recoverySafe": true`) || !fake.closed {
		t.Fatalf("output=%s closed=%t", output.String(), fake.closed)
	}
}

func TestPluginCommandCLIRunsJSONInputAndOutput(t *testing.T) {
	fake := &fakePluginCommandConsole{result: pluginCommandRunResult{
		CommandID: "demo.commands.command.sync", ContractVersion: "demo.commands.command.sync@1",
		ExtensionID: "demo.commands", ExtensionVersion: "1.0.0", ArtifactDigest: strings.Repeat("a", 64),
		Output: map[string]any{"ok": true},
	}}
	withPluginCommandConsole(t, fake)
	root := newRootCommand()
	root.SetArgs([]string{"extension", "command", "run", "demo.commands.command.sync", "--input", `{"name":"SForum"}`})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if fake.input["name"] != "SForum" || !strings.Contains(output.String(), `"ok": true`) {
		t.Fatalf("input=%#v output=%s", fake.input, output.String())
	}
}

func TestPluginCommandCLIPropagatesSafeModePolicy(t *testing.T) {
	fake := &fakePluginCommandConsole{err: extensionsruntime.ErrPluginCommandSafeMode}
	withPluginCommandConsole(t, fake)
	root := newRootCommand()
	root.SetArgs([]string{"extension", "command", "run", "demo.commands.command.sync", "--safe-mode"})
	if err := root.Execute(); !errors.Is(err, extensionsruntime.ErrPluginCommandSafeMode) {
		t.Fatalf("safe mode error = %v", err)
	}
}

type fakeCLIPluginStarter struct {
	starts int
}

func (s *fakeCLIPluginStarter) Start(context.Context, extensions.Extension) (extensionsruntime.RouteTarget, error) {
	s.starts++
	return extensionsruntime.RouteTarget{InstanceID: "runtime-cli"}, nil
}

func (*fakeCLIPluginStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (*fakeCLIPluginStarter) InvokePluginCommand(
	context.Context,
	extensionsruntime.RuntimeInstanceIdentity,
	extensionsruntime.PluginCommandContract,
	map[string]any,
) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

func TestProductionPluginCommandConsoleEnforcesSafeModeBeforeRuntimeStart(t *testing.T) {
	ordinary := cliPluginCommandExtension("demo.ordinary", false)
	starter := &fakeCLIPluginStarter{}
	console := testPluginCommandConsole(t, starter, ordinary, true)
	if _, err := console.Run(context.Background(), ordinary.Manifest.Commands[0].ID, map[string]any{}); !errors.Is(err, extensionsruntime.ErrPluginCommandSafeMode) {
		t.Fatalf("ordinary safe mode error = %v", err)
	}
	if starter.starts != 0 {
		t.Fatalf("ordinary safe mode started %d runtime(s)", starter.starts)
	}

	recovery := cliPluginCommandExtension("demo.recovery", true)
	console = testPluginCommandConsole(t, starter, recovery, true)
	result, err := console.Run(context.Background(), recovery.Manifest.Commands[0].ID, map[string]any{})
	if err != nil || result.Output["ok"] != true || starter.starts != 1 {
		t.Fatalf("recovery result=%#v starts=%d err=%v", result, starter.starts, err)
	}
}

func TestPluginCommandCLIRejectsNonObjectAndAmbiguousInput(t *testing.T) {
	if _, err := decodePluginCommandInput(pluginCommandRunOptions{Input: "null"}); err == nil {
		t.Fatal("null input was accepted")
	}
	if _, err := decodePluginCommandInput(pluginCommandRunOptions{Input: `{}`, InputFile: "input.json"}); err == nil {
		t.Fatal("ambiguous input was accepted")
	}
}

func withPluginCommandConsole(t *testing.T, fake pluginCommandConsole) {
	t.Helper()
	previous := openPluginCommandConsole
	openPluginCommandConsole = func(context.Context, pluginCommandOptions) (pluginCommandConsole, error) { return fake, nil }
	t.Cleanup(func() { openPluginCommandConsole = previous })
}

func testPluginCommandConsole(
	t *testing.T,
	starter *fakeCLIPluginStarter,
	extension extensions.Extension,
	safeMode bool,
) *postgresPluginCommandConsole {
	t.Helper()
	catalog := extensionsruntime.NewPluginCommandRegistry()
	if err := catalog.ReplaceRuntime(extension, "catalog:"+extension.PackageDigest); err != nil {
		t.Fatal(err)
	}
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	return &postgresPluginCommandConsole{
		manager: manager, auditor: audit.NoopWriter{}, catalog: catalog,
		extensions: map[string]extensions.Extension{extension.ID: extension}, safeMode: safeMode,
	}
}

func cliPluginCommandExtension(id string, recoverySafe bool) extensions.Extension {
	return extensions.Extension{
		ID: id, Version: "1.0.0", Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackageDigest: strings.Repeat("a", 64),
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2},
			Commands: []extensions.ManifestCommand{{
				ID: id + ".command.sync", ContractVersion: id + ".command.sync@1", Handler: "command.sync",
				InputSchema: id + ".command.input@1", ResultSchema: id + ".command.result@1",
				RecoverySafe: recoverySafe, TimeoutMS: 3000,
			}},
		},
	}
}
