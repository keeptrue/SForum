package extensionscontroller

import (
	"errors"
	"net/http"
	"testing"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestMapExecutablePlatformErrorSplitsInstallFromRuntime(t *testing.T) {
	tests := []struct {
		name   string
		stage  string
		status int
	}{
		{name: "install is the package's own defect", stage: extensions.CoreCompatibilityStageInstall, status: http.StatusUnprocessableEntity},
		{name: "runtime is a state conflict", stage: extensions.CoreCompatibilityStageRuntime, status: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapExtensionError(errors.Join(&extensions.ExecutablePlatformMismatchError{
				Entry:    "backend/plugin",
				Required: "any/arm64",
				Host:     "linux/amd64",
				Stage:    test.stage,
			}, errors.New("private package path")))
			apiErr, ok := mapped.(*apphttp.APIError)
			if !ok {
				t.Fatalf("expected API error, got %T", mapped)
			}
			if apiErr.Status != test.status || apiErr.Reason != extensions.CodePlatformIncompatible {
				t.Fatalf("unexpected mapping: %#v", apiErr)
			}
			if got := apiErr.Fields["required"]; len(got) != 1 || got[0] != "any/arm64" {
				t.Fatalf("required fields = %#v", apiErr.Fields)
			}
			if got := apiErr.Fields["host"]; len(got) != 1 || got[0] != "linux/amd64" {
				t.Fatalf("host fields = %#v", apiErr.Fields)
			}
			if got := apiErr.Fields["entry"]; len(got) != 1 || got[0] != "backend/plugin" {
				t.Fatalf("entry fields = %#v", apiErr.Fields)
			}
			if _, leaked := apiErr.Details["private"]; leaked {
				t.Fatalf("internal detail must not leak: %#v", apiErr.Details)
			}
		})
	}
}

func TestMapExecutablePlatformErrorIgnoresUnrelatedErrors(t *testing.T) {
	if mapped := mapExecutablePlatformError(errors.New("something else")); mapped != nil {
		t.Fatalf("expected nil mapping, got %#v", mapped)
	}
	// 包装后仍能命中：errors.As 走 Unwrap。
	wrapped := errors.Join(errors.New("outer"), &extensions.ExecutablePlatformMismatchError{
		Entry: "backend/plugin", Required: "any/arm64", Host: "linux/amd64",
		Stage: extensions.CoreCompatibilityStageRuntime,
	})
	if mapped := mapExecutablePlatformError(wrapped); mapped == nil {
		t.Fatal("expected the wrapped mismatch to be mapped")
	}
}

func TestMapCoreIncompatibleErrorStaysBehaviourallyIdentical(t *testing.T) {
	// 搬文件不能改变既有 coreVersion 映射：422/409 与三个字段都保持原样。
	mapped := mapExtensionError(&extensions.CoreIncompatibleError{
		Required: "^9.0.0", CoreVersion: "3.0.16", BuildVersion: "dev", Stage: extensions.CoreCompatibilityStageInstall,
	})
	apiErr, ok := mapped.(*apphttp.APIError)
	if !ok {
		t.Fatalf("expected API error, got %T", mapped)
	}
	if apiErr.Status != http.StatusUnprocessableEntity || apiErr.Reason != extensions.CodeCoreIncompatible {
		t.Fatalf("unexpected mapping: %#v", apiErr)
	}
	for _, field := range []string{"required", "coreVersion", "buildVersion"} {
		if len(apiErr.Fields[field]) != 1 {
			t.Fatalf("missing field %q in %#v", field, apiErr.Fields)
		}
	}
}
