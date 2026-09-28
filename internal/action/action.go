// Package action adapts the CLI to GitHub Actions without shell parsing or jq.
package action

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cadenya/cadenya-config-loader/internal/command"
)

func Run(ctx context.Context, version string, getenv func(string) string, out, errOut io.Writer) error {
	mode := getenv("INPUT_COMMAND")
	if mode == "" {
		mode = "plan"
	}
	if mode != "plan" && mode != "apply" && mode != "validate" {
		return command.Usagef("command must be validate, plan, or apply")
	}
	allowEmpty := getenv("INPUT_ALLOW_EMPTY")
	if allowEmpty != "" && allowEmpty != "true" && allowEmpty != "false" {
		return command.Usagef("allow-empty must be true or false")
	}
	if getenv("GITHUB_OUTPUT") == "" {
		return command.Usagef("GITHUB_OUTPUT is required; use cadenya-config directly outside GitHub Actions")
	}
	outputs, err := os.OpenFile(getenv("GITHUB_OUTPUT"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open GitHub outputs: %w", err)
	}
	defer outputs.Close()
	var summary *os.File
	if path := getenv("GITHUB_STEP_SUMMARY"); path != "" {
		summary, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("open GitHub summary: %w", err)
		}
		defer summary.Close()
	}
	report, err := os.CreateTemp(getenv("RUNNER_TEMP"), "cadenya-result-*.json")
	if err != nil {
		return err
	}
	if err := report.Close(); err != nil {
		return err
	}
	args := []string{"cadenya-config", mode, "--output", "json", "--report-file", report.Name()}
	for _, input := range []struct{ name, flag string }{
		{"DIRECTORY", "directory"}, {"API_KEY", "api-key"}, {"WORKSPACE_ID", "workspace-id"},
		{"BUNDLE_KEY", "bundle-key"}, {"BASE_URL", "base-url"}, {"CONFIG", "config"},
		{"RESOURCE_DIR", "resource-dir"}, {"TIMEOUT", "timeout"}, {"OPERATION_TIMEOUT", "operation-timeout"}, {"RETRIES", "retries"},
		{"LOG_LEVEL", "log-level"}, {"LOG_FORMAT", "log-format"},
	} {
		if value := getenv("INPUT_" + input.name); value != "" {
			args = append(args, "--"+input.flag, value)
		}
	}
	if mode == "apply" && allowEmpty == "true" {
		args = append(args, "--allow-empty")
	}
	var buffer bytes.Buffer
	cliErr := command.New(version, &buffer, errOut).Run(ctx, args)
	data := buffer.Bytes()
	var result command.Report
	if err := json.Unmarshal(data, &result); err != nil {
		// Flag parse errors occur before the command's report handler.
		if cliErr == nil {
			return fmt.Errorf("CLI did not produce a JSON report: %w", err)
		}
		message := cliErr.Error()
		if key := getenv("INPUT_API_KEY"); key != "" {
			message = strings.ReplaceAll(message, key, "[REDACTED]")
		}
		result = command.Report{SchemaVersion: 1, Command: mode, Error: message}
		data, err = json.Marshal(result)
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if err := os.WriteFile(report.Name(), data, 0600); err != nil {
			return errors.Join(cliErr, err)
		}
	}
	_, logErr := out.Write(data)
	_, outputErr := fmt.Fprintf(outputs, "report-path=%s\nresult=%s\n", report.Name(), strings.TrimSpace(string(data)))
	var creates, updates, deletes, detaches, stateChanges int
	if result.Plan != nil {
		creates, updates, deletes, detaches, stateChanges = result.Summary.Creates, result.Summary.Updates, result.Summary.Deletes, result.Summary.Detaches, result.Summary.StateChanges
	}
	_, countErr := fmt.Fprintf(outputs, "creates=%d\nupdates=%d\ndeletes=%d\ndetaches=%d\nstate-changes=%d\n", creates, updates, deletes, detaches, stateChanges)
	var summaryErr error
	if summary != nil {
		status := "Succeeded"
		if cliErr != nil {
			status = "Failed"
		}
		_, summaryErr = fmt.Fprintf(summary, "### Cadenya configuration\n\n%s: %s\n\n| Create | Update | Delete | Detach | State change |\n| --- | --- | --- | --- | --- |\n| %d | %d | %d | %d | %d |\n", mode, status, creates, updates, deletes, detaches, stateChanges)
	}
	return errors.Join(cliErr, logErr, outputErr, countErr, summaryErr)
}
