package action

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type actionMetadata struct {
	Inputs map[string]struct {
		Description string
		Default     string
	}
	Outputs map[string]struct{ Value string }
	Runs    struct {
		Using string
		Steps []struct {
			ID   string
			Uses string
			Run  string
			Env  map[string]string
		}
	}
}

func readAction(t *testing.T) actionMetadata {
	t.Helper()
	data, err := os.ReadFile("../../action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var action actionMetadata
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("action.yml does not parse: %v", err)
	}
	return action
}

// TestActionMetadataMatchesAdapter keeps action.yml, which no Go test executes,
// in step with the adapter that reads its inputs and writes its outputs.
func TestActionMetadataMatchesAdapter(t *testing.T) {
	action := readAction(t)
	source, err := os.ReadFile("action.go")
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]map[string]string{}
	for _, step := range action.Runs.Steps {
		steps[step.ID] = step.Env
	}
	install, run := steps["install"], steps["run"]
	if action.Runs.Using != "composite" || install == nil || run == nil {
		t.Fatal("expected a composite action with steps install and run")
	}
	// The install step consumes these; everything else goes to the adapter.
	installInputs := map[string]string{"version": "CADENYA_VERSION", "binary-path": "CADENYA_BINARY"}
	for name, input := range action.Inputs {
		if input.Description == "" {
			t.Errorf("input %s has no description", name)
		}
		if env, ok := installInputs[name]; ok {
			if install[env] != "${{ inputs."+name+" }}" {
				t.Errorf("input %s is not passed to the install step as %s", name, env)
			}
			continue
		}
		env := "INPUT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if run[env] != "${{ inputs."+name+" }}" {
			t.Errorf("input %s is not passed to the adapter as %s", name, env)
		}
		suffix := strings.TrimPrefix(env, "INPUT_")
		if !strings.Contains(string(source), `"`+env+`"`) && !strings.Contains(string(source), `"`+suffix+`"`) {
			t.Errorf("the adapter never reads %s", env)
		}
	}
	for name, output := range action.Outputs {
		if output.Value != "${{ steps.run.outputs."+name+" }}" {
			t.Errorf("output %s must come from the run step: %s", name, output.Value)
		}
		if !strings.Contains(string(source), name+"=") {
			t.Errorf("the adapter never writes output %s", name)
		}
	}
}

// TestActionRunsTheRelease keeps the action on released binaries. Building
// from source on every run costs every consumer a Go install and a compile.
func TestActionRunsTheRelease(t *testing.T) {
	action := readAction(t)
	for _, step := range action.Runs.Steps {
		if strings.Contains(step.Uses, "setup-go") || strings.Contains(step.Run, "go build") {
			t.Errorf("step %q builds from source; the action must download its release", step.ID)
		}
	}
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	latest := regexp.MustCompile(`(?m)^## v(\S+)`).FindSubmatch(changelog)
	if latest == nil {
		t.Fatal("CHANGELOG.md has no release section")
	}
	if got := action.Inputs["version"].Default; got != string(latest[1]) {
		t.Errorf("action.yml's version input defaults to %q, but the latest release in CHANGELOG.md is %s; bump both together", got, latest[1])
	}
}
