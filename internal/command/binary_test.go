package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func buildCLI(t *testing.T, name string) string {
	t.Helper()
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, "./cmd/"+name)
	build.Dir = project
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return binary
}

func runBinaryReport(t *testing.T, binary, root, mode string, wantFailure bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, mode, "--report-file", "result.json")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if (err != nil) != wantFailure {
		t.Fatalf("%s: %v\n%s", mode, err, output)
	}
	if wantFailure {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("expected exit code 1, got %v", err)
		}
	}
	if strings.Contains(string(output), "test-key") {
		t.Fatal("credential logged")
	}
	data, err := os.ReadFile(filepath.Join(root, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result Report
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.Command != mode || (result.Error != "") != wantFailure {
		t.Fatal(string(data))
	}
	if mode == "apply" && !wantFailure && (result.Plan == nil || !result.Applied) {
		t.Fatal("apply report did not record success")
	}
}

// cleanEnv drops a developer's Cadenya settings so they can't leak into a test.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CADENYA_") {
			env = append(env, kv)
		}
	}
	return env
}

func TestExitCodes(t *testing.T) {
	binary := buildCLI(t, "cadenya-config")
	root := localBundle(t)
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"valid bundle", []string{"validate"}, ExitOK},
		{"unknown command", []string{"deploy"}, ExitUsage},
		{"unknown flag", []string{"plan", "--nope"}, ExitUsage},
		{"extra argument", []string{"validate", "extra"}, ExitUsage},
		{"bad log level", []string{"--log-level", "loud", "validate"}, ExitUsage},
		{"missing API key", []string{"plan"}, ExitUsage},
		{"invalid bundle", []string{"--resource-dir", "missing", "validate"}, ExitFailure},
		{"unreachable API", []string{"--api-key", "k", "--base-url", "http://127.0.0.1:1", "--retries", "0", "plan"}, ExitFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binary, append([]string{"-C", root}, tc.args...)...)
			cmd.Env = cleanEnv()
			out, err := cmd.CombinedOutput()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.want {
				t.Fatalf("exit %d, want %d\n%s", code, tc.want, out)
			}
		})
	}
}

func TestInterruptExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt cannot be sent to a process on Windows")
	}
	binary := buildCLI(t, "cadenya-config")
	root := localBundle(t)
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done() // Hang until the client gives up.
	}))
	defer server.Close()
	cmd := exec.Command(binary, "-C", root, "--api-key", "k", "--base-url", server.URL, "plan")
	cmd.Env = cleanEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("CLI never reached the API")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != ExitInterrupted {
		t.Fatalf("want exit %d, got %v\n%s", ExitInterrupted, err, stderr.String())
	}
}
