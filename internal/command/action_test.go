package command

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestActionWrapper(t *testing.T) {
	binary := buildCLI(t, "cadenya-config")
	for _, scenario := range []string{"plan", "apply", "validate", "failed apply", "invalid command", "invalid YAML", "environment fallback"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			root := localBundle(t)
			withSpaces := filepath.Join(t.TempDir(), "repo with spaces")
			if err := os.Rename(root, withSpaces); err != nil {
				t.Fatal(err)
			}
			root = withSpaces
			dir := t.TempDir()
			output := filepath.Join(dir, "outputs")
			summary := filepath.Join(dir, "summary")
			t.Setenv("RUNNER_TEMP", dir)
			t.Setenv("GITHUB_OUTPUT", output)
			t.Setenv("GITHUB_STEP_SUMMARY", summary)
			t.Setenv("INPUT_API_KEY", "test-key")
			t.Setenv("INPUT_BASE_URL", f.server.URL)
			t.Setenv("INPUT_DIRECTORY", root)
			t.Setenv("INPUT_BUNDLE_KEY", "test")
			t.Setenv("INPUT_WORKSPACE_ID", "development")
			t.Setenv("INPUT_CONFIG", "")
			t.Setenv("INPUT_RESOURCE_DIR", "")
			t.Setenv("INPUT_TIMEOUT", "2s")
			t.Setenv("INPUT_ALLOW_EMPTY", "false")
			mode := scenario
			if scenario == "failed apply" {
				mode = "apply"
				f.setFailure(func(r *http.Request) bool { return r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/tools") })
			}
			if scenario == "invalid command" {
				mode = "apply; touch injected"
			}
			if scenario == "invalid YAML" {
				mode = "apply"
				put(t, root, ".cadenya/agents/broken.yaml", "spec: {unknown: true}")
			}
			if scenario == "environment fallback" {
				mode = "plan"
				t.Setenv("INPUT_API_KEY", "")
				t.Setenv("INPUT_BASE_URL", "")
				t.Setenv("INPUT_WORKSPACE_ID", "")
				t.Setenv("INPUT_BUNDLE_KEY", "")
				t.Setenv("CADENYA_API_KEY", "test-key")
				t.Setenv("CADENYA_BASE_URL", f.server.URL)
			}
			t.Setenv("INPUT_COMMAND", mode)
			cmd := exec.Command(binary, "github-action")
			cmd.Dir = root
			logs, err := cmd.CombinedOutput()
			shouldFail := scenario == "failed apply" || scenario == "invalid command" || scenario == "invalid YAML"
			if (err != nil) != shouldFail {
				t.Fatalf("error=%v\n%s", err, logs)
			}
			if strings.Contains(string(logs), "test-key") {
				t.Fatal("API key appeared in logs")
			}
			if scenario == "invalid command" {
				if len(f.calls()) != 0 {
					t.Fatal("invalid command reached API")
				}
				if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
					t.Fatal("input was executed as shell source")
				}
				return
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				k, v, _ := strings.Cut(line, "=")
				values[k] = v
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(values["result"]), &result); err != nil {
				t.Fatalf("invalid action JSON: %v: %s", err, data)
			}
			if _, err := os.Stat(values["report-path"]); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(summary); err != nil {
				t.Fatal(err)
			}
			if scenario == "validate" {
				if len(f.calls()) != 0 {
					t.Fatal("validate reached API")
				}
				return
			}
			if scenario == "invalid YAML" {
				if result["error"] == nil || values["creates"] != "0" || len(f.calls()) != 0 {
					t.Fatalf("invalid YAML report: %s", data)
				}
				return
			}
			if values["creates"] != "4" || values["deletes"] != "0" {
				t.Fatalf("bad counts: %s", data)
			}
			if scenario == "plan" && len(mutations(f)) != 0 {
				t.Fatal("action plan wrote to API")
			}
			if scenario == "apply" && result["applied"] != true {
				t.Fatal(values["result"])
			}
			if scenario == "failed apply" && result["error"] == nil {
				t.Fatal("action lost partial failure report")
			}
		})
	}
}
