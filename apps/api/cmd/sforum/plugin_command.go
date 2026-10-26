package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

type pluginCommandOptions struct {
	DatabaseURL string
	SafeMode    bool
}

type pluginCommandRunOptions struct {
	Input     string
	InputFile string
}

type pluginCommandDescriptor struct {
	ID               string `json:"id"`
	ContractVersion  string `json:"contractVersion"`
	ExtensionID      string `json:"extensionId"`
	ExtensionVersion string `json:"extensionVersion"`
	ArtifactDigest   string `json:"artifactDigest"`
	Description      string `json:"description,omitempty"`
	Permission       string `json:"permission,omitempty"`
	InputSchema      string `json:"inputSchema"`
	ResultSchema     string `json:"resultSchema"`
	RecoverySafe     bool   `json:"recoverySafe"`
	Available        bool   `json:"available"`
	TimeoutMS        int64  `json:"timeoutMs"`
}

type pluginCommandRunResult struct {
	CommandID        string         `json:"commandId"`
	ContractVersion  string         `json:"contractVersion"`
	ExtensionID      string         `json:"extensionId"`
	ExtensionVersion string         `json:"extensionVersion"`
	ArtifactDigest   string         `json:"artifactDigest"`
	Output           map[string]any `json:"output"`
}

type pluginCommandConsole interface {
	List(context.Context) ([]pluginCommandDescriptor, error)
	Run(context.Context, string, map[string]any) (pluginCommandRunResult, error)
	Close(context.Context)
}

var openPluginCommandConsole = openPostgresPluginCommandConsole

func newPluginCommandCommand() *cobra.Command {
	opts := pluginCommandOptions{}
	cmd := &cobra.Command{
		Use:   "command",
		Short: "List and run trusted plugin commands",
	}
	cmd.PersistentFlags().StringVar(&opts.DatabaseURL, "database-url", "", "PostgreSQL URL (defaults to DATABASE_URL)")
	cmd.PersistentFlags().BoolVar(&opts.SafeMode, "safe-mode", false, "Enforce Safe Mode in addition to SFORUM_SAFE_MODE")
	cmd.AddCommand(newPluginCommandListCommand(&opts), newPluginCommandRunCommand(&opts))
	return cmd
}

func newPluginCommandListCommand(opts *pluginCommandOptions) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the validated command namespace without starting plugin code",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			console, err := openPluginCommandConsole(cmd.Context(), *opts)
			if err != nil {
				return err
			}
			defer console.Close(context.Background())
			commands, err := console.List(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return encodePluginCommandJSON(cmd, commands)
			}
			cmd.Println("ID\tEXTENSION\tVERSION\tSAFE MODE\tINPUT")
			for _, command := range commands {
				safeMode := "blocked"
				if command.RecoverySafe {
					safeMode = "recovery-safe"
				}
				cmd.Printf("%s\t%s\t%s\t%s\t%s\n", command.ID, command.ExtensionID, command.ExtensionVersion, safeMode, command.InputSchema)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON")
	return cmd
}

func newPluginCommandRunCommand(opts *pluginCommandOptions) *cobra.Command {
	runOpts := pluginCommandRunOptions{}
	cmd := &cobra.Command{
		Use:   "run <command-id>",
		Short: "Run one exact trusted plugin command",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := decodePluginCommandInput(runOpts)
			if err != nil {
				return err
			}
			console, err := openPluginCommandConsole(cmd.Context(), *opts)
			if err != nil {
				return err
			}
			defer console.Close(context.Background())
			result, err := console.Run(cmd.Context(), strings.TrimSpace(args[0]), input)
			if err != nil {
				return err
			}
			return encodePluginCommandJSON(cmd, result)
		},
	}
	cmd.Flags().StringVar(&runOpts.Input, "input", "", "JSON object input (defaults to {})")
	cmd.Flags().StringVar(&runOpts.InputFile, "input-file", "", "Read the JSON object input from a file")
	return cmd
}

func decodePluginCommandInput(opts pluginCommandRunOptions) (map[string]any, error) {
	if strings.TrimSpace(opts.Input) != "" && strings.TrimSpace(opts.InputFile) != "" {
		return nil, errors.New("plugin command input: use only one of --input and --input-file")
	}
	body := []byte(strings.TrimSpace(opts.Input))
	if path := strings.TrimSpace(opts.InputFile); path != "" {
		var err error
		body, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read plugin command input: %w", err)
		}
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return map[string]any{}, nil
	}
	var input map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("decode plugin command input: %w", err)
	}
	if input == nil {
		return nil, errors.New("plugin command input must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("plugin command input must contain one JSON object")
	}
	return input, nil
}

func encodePluginCommandJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func pluginCommandDescriptorFromContract(contract extensionsruntime.PluginCommandContract, safeMode bool) pluginCommandDescriptor {
	return pluginCommandDescriptor{
		ID: contract.ID, ContractVersion: contract.ContractVersion,
		ExtensionID: contract.ExtensionID, ExtensionVersion: contract.ExtensionVersion,
		ArtifactDigest: contract.ArtifactDigest, Description: contract.Description,
		Permission: contract.Permission, InputSchema: contract.InputSchema, ResultSchema: contract.ResultSchema,
		RecoverySafe: contract.RecoverySafe, Available: !safeMode || contract.RecoverySafe,
		TimeoutMS: contract.Timeout.Milliseconds(),
	}
}
