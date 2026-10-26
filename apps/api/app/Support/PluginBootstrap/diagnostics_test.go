package pluginbootstrap_test

import (
	"errors"
	"strings"
	"testing"

	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
)

func TestClassifyHistoricalCookieMismatch(t *testing.T) {
	diagnostic := "This binary is a plugin. These are not meant to be executed directly."
	err := pluginbootstrap.ClassifyStartError(
		errors.New("failed to read any lines from plugin stdout"),
		diagnostic,
	)
	if !errors.Is(err, pluginbootstrap.ErrBootstrapABIIncompatible) {
		t.Fatalf("classification=%v", err)
	}
	if !strings.Contains(err.Error(), diagnostic) {
		t.Fatalf("diagnostic missing from internal error=%v", err)
	}
}

func TestClassifyStartErrorIgnoresGoPluginPossibilityList(t *testing.T) {
	err := pluginbootstrap.ClassifyStartError(errors.New(`Unrecognized remote plugin message:
This usually means the plugin was not compiled for this architecture,
the plugin is missing dynamic-link libraries necessary to run,
or the plugin is not executable by this process due to file permissions.`), "")
	if !errors.Is(err, pluginbootstrap.ErrProcessStart) {
		t.Fatalf("classification=%v", err)
	}
}

func TestClassifyExplicitDynamicDependencyFailure(t *testing.T) {
	err := pluginbootstrap.ClassifyStartError(
		errors.New("failed to read any lines from plugin stdout"),
		"dyld[42]: Library not loaded: /opt/libexample.dylib",
	)
	if !errors.Is(err, pluginbootstrap.ErrExecutableDependency) {
		t.Fatalf("classification=%v", err)
	}
}

func TestDiagnosticBufferIsBounded(t *testing.T) {
	buffer := pluginbootstrap.NewDiagnosticBuffer(8)
	_, _ = buffer.Write([]byte("123456"))
	_, _ = buffer.Write([]byte("7890"))
	if got := buffer.String(); got != "12345678" {
		t.Fatalf("buffer=%q", got)
	}
	if got := pluginbootstrap.SanitizedDiagnostic("line\x00\nnext"); strings.ContainsRune(got, '\x00') {
		t.Fatalf("diagnostic=%q", got)
	}
}
