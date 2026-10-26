package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	apilts "github.com/keeptrue/sforum/apps/api/app/Support/APILTS"
)

func TestExtensionAPILTSCommandJSON(t *testing.T) {
	apilts.ResetProcessForTest(apilts.New())
	t.Cleanup(func() { apilts.ResetProcessForTest(nil) })

	cmd := newExtensionAPILTSCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v\n%s", err, buf.String())
	}
	if payload["schemaVersion"] != apilts.SchemaVersion {
		t.Fatalf("schemaVersion = %#v", payload["schemaVersion"])
	}
	if _, exists := payload["protocolV1Calls"]; exists {
		t.Fatalf("removed protocol telemetry remains in payload: %#v", payload)
	}
	if payload["themeRequestTimeLoaderCalls"] != float64(0) {
		t.Fatalf("themeRequestTimeLoaderCalls = %#v", payload["themeRequestTimeLoaderCalls"])
	}
}

func TestExtensionAPILTSCommandText(t *testing.T) {
	apilts.ResetProcessForTest(apilts.New())
	t.Cleanup(func() { apilts.ResetProcessForTest(nil) })

	root := &cobra.Command{Use: "sforum"}
	root.AddCommand(func() *cobra.Command {
		ext := &cobra.Command{Use: "extension"}
		ext.AddCommand(newExtensionAPILTSCommand())
		return ext
	}())
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"extension", "api-lts"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"schema: " + apilts.SchemaVersion,
		apilts.ThemeRequestTimeLoaderContractID,
		"themeRequestTimeLoaderCalls: 0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
