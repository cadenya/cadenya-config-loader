package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cadenya/cadenya-config-loader/internal/reconcile"
	"github.com/urfave/cli/v3"
)

// Report is the versioned machine-readable contract shared by every CI platform.
// Additive fields may be introduced within a schema version.
type Report struct {
	*reconcile.Plan
	SchemaVersion int    `json:"schemaVersion"`
	Command       string `json:"command"`
	Valid         *bool  `json:"valid,omitempty"`
	Resources     *int   `json:"resources,omitempty"`
	Error         string `json:"error,omitempty"`
}

func run(ctx context.Context, cmd *cli.Command) error {
	format := cmd.String("output")
	if format != "text" && format != "json" {
		return usagef("--output must be text or json")
	}
	result := &Report{SchemaVersion: 1, Command: cmd.Name}
	file, err := prepareReport(cmd.String("report-file"))
	if file != nil {
		defer os.Remove(file.Name())
	}
	if err != nil {
		err = usageError{err} // An unusable --report-file destination.
	}
	if err == nil {
		err = execute(ctx, cmd, result)
	}
	err = redact(err, cmd.String("api-key"))
	if err != nil {
		result.Error = err.Error()
	}
	data, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		return errors.Join(err, encodeErr)
	}
	data = append(data, '\n')
	var fileErr error
	if file != nil {
		fileErr = commitReport(file, cmd.String("report-file"), data)
	}
	var outputErr error
	if format == "json" {
		_, outputErr = cmd.Root().Writer.Write(data)
	} else {
		outputErr = result.text(cmd.Root().Writer)
	}
	return errors.Join(err, fileErr, outputErr)
}

type redactedError struct {
	cause   error
	message string
}

func (e redactedError) Error() string { return e.message }
func (e redactedError) Unwrap() error { return e.cause }
func redact(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return redactedError{err, strings.ReplaceAll(err.Error(), secret, "[REDACTED]")}
}

// Open the output location before API calls, so bad paths cannot fail only after
// resources have changed. Rename keeps a previously complete report intact.
func prepareReport(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("report destination must be a regular file: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cadenya-report-*.json")
	if err != nil {
		return nil, fmt.Errorf("prepare report: %w", err)
	}
	return f, nil
}

func commitReport(f *os.File, path string, data []byte) error {
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("save report: %w", err)
	}
	return nil
}

func (r *Report) text(out io.Writer) error {
	if r.Valid != nil && *r.Valid {
		_, err := fmt.Fprintf(out, "Valid bundle %s: %d resources\n", r.BundleKey, *r.Resources)
		return err
	}
	if r.Plan == nil {
		return nil
	}
	for _, op := range r.Operations {
		line := fmt.Sprintf("%-9s %s", op.Action, op.Key)
		if op.Host != "" {
			line += " (" + op.Host + ")"
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	status := "Plan"
	if r.Applied {
		status = "Applied"
	} else if r.Error != "" {
		status = "Apply failed"
	}
	line := fmt.Sprintf("%s: %d create, %d update, %d delete, %d detach", status, r.Summary.Creates, r.Summary.Updates, r.Summary.Deletes, r.Summary.Detaches)
	if r.Summary.StateChanges > 0 {
		line += fmt.Sprintf(", %d state change", r.Summary.StateChanges)
	}
	_, err := fmt.Fprintln(out, line)
	return err
}
