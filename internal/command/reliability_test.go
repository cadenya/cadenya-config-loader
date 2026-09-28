package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReportIncludesPreflightErrors(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	put(t, root, ".cadenya/agents/broken.yaml", "spec: {typo: true}")
	path := filepath.Join(t.TempDir(), "result.json")
	out, _, err := invoke(t, f, root, "apply", "--report-file", path)
	if err == nil {
		t.Fatal("invalid YAML accepted")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal([]byte(out), data) {
		t.Fatalf("stdout and report differ: %s / %s", out, data)
	}
	var result Report
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.Command != "apply" || result.Error == "" {
		t.Fatal(string(data))
	}
	if len(f.calls()) != 0 {
		t.Fatal("invalid bundle reached API")
	}
}

func TestReportDestinationCheckedBeforeMutations(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	for _, path := range []string{t.TempDir(), filepath.Join(t.TempDir(), "absent", "result.json")} {
		if _, _, err := invoke(t, f, root, "apply", "--report-file", path); err == nil {
			t.Fatal("invalid output path accepted")
		}
	}
	if len(f.calls()) != 0 {
		t.Fatal("invalid output path reached API")
	}
}

func TestRepeatedCursorStopsBeforeWrites(t *testing.T) {
	f := newFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" {
			t.Error("unexpected mutation")
		}
		io.WriteString(w, `{"items":[],"pagination":{"nextCursor":"repeated"}}`)
	}))
	defer server.Close()
	f.server = server
	_, _, err := invoke(t, f, localBundle(t), "apply")
	if err == nil || !strings.Contains(err.Error(), "repeated a pagination cursor") {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("pagination was not bounded: %d requests", requests.Load())
	}
}

func TestOperationDeadlineCancelsDiscovery(t *testing.T) {
	f := newFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	f.server = server
	out, _, err := invoke(t, f, localBundle(t), "apply", "--timeout", "10s", "--operation-timeout", "20ms")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if !strings.Contains(out, `"error":`) {
		t.Fatal("deadline failure omitted JSON report")
	}
}

func TestCancellationStopsBeforeAPI(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := New("test", io.Discard, io.Discard)
	err := cmd.Run(ctx, []string{"cadenya-config", "apply", "-C", localBundle(t), "--base-url", f.server.URL, "--api-key", "test-key"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if len(f.calls()) != 0 {
		t.Fatal("cancelled command reached API")
	}
}

func TestRetriesOnlyForIdempotentRequests(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			f := newFixture(t)
			root := localBundle(t)
			if method == "PATCH" {
				if _, _, err := invoke(t, f, root, "apply"); err != nil {
					t.Fatal(err)
				}
			}
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == method && strings.Contains(r.URL.Path, "/tool_sets") {
					n := attempts.Add(1)
					if n <= 2 {
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(429)
						io.WriteString(w, `{"code":8,"message":"rate limited"}`)
						return
					}
				}
				f.serve(w, r)
			}))
			defer server.Close()
			f.server = server
			_, _, err := invoke(t, f, root, "apply", "--retries", "2")
			if method == "GET" {
				if err != nil || attempts.Load() < 3 {
					t.Fatalf("GET was not retried: %v (%d)", err, attempts.Load())
				}
			} else {
				if err == nil || attempts.Load() != 1 {
					t.Fatalf("mutation must not be retried: %v (%d)", err, attempts.Load())
				}
			}
		})
	}
}

func TestAPIKeyRedactedFromErrors(t *testing.T) {
	f := newFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"code":3,"message":"the submitted token test-key is invalid"}`)
	}))
	defer server.Close()
	f.server = server
	out, stderr, err := invoke(t, f, localBundle(t), "apply")
	if err == nil {
		t.Fatal("expected API error")
	}
	if strings.Contains(out+stderr+err.Error(), "test-key") {
		t.Fatal("credential appeared in diagnostic output")
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatal(out)
	}
}

// This executes the real binary using only the environment and a report path,
// as a Buildkite command step does. No GitHub variables or adapter are involved.
func TestBuildkiteCommandContract(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	binary := buildCLI(t, "cadenya-config")
	t.Setenv("CADENYA_API_KEY", "test-key")
	t.Setenv("CADENYA_BASE_URL", f.server.URL)
	t.Setenv("BUILDKITE", "true")
	t.Setenv("BUILDKITE_BUILD_NUMBER", "123")
	t.Setenv("GITHUB_OUTPUT", "")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	for _, mode := range []string{"validate", "plan", "apply"} {
		runBinaryReport(t, binary, root, mode, false)
	}
	f.setFailure(func(r *http.Request) bool { return r.Method == "PATCH" })
	runBinaryReport(t, binary, root, "apply", true)
}
