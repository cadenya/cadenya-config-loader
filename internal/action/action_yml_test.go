package action

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestActionMetadataMatchesAdapter keeps action.yml, which no Go test executes,
// in step with the adapter that reads its inputs and writes its outputs.
func TestActionMetadataMatchesAdapter(t *testing.T) {
	data, err := os.ReadFile("../../action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Inputs  map[string]struct{ Description string }
		Outputs map[string]struct{ Value string }
		Runs    struct {
			Using string
			Steps []struct {
				ID   string
				Uses string
				With map[string]string
				Env  map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("action.yml does not parse: %v", err)
	}
	source, err := os.ReadFile("action.go")
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]string
	for _, step := range action.Runs.Steps {
		if step.ID == "run" {
			run = step.Env
		}
		if strings.HasPrefix(step.Uses, "actions/setup-go@") && step.With["go-version"] != "${{ inputs.go-version }}" {
			t.Errorf("setup-go must build with inputs.go-version: %v", step.With)
		}
	}
	if action.Runs.Using != "composite" || run == nil {
		t.Fatal("expected a composite action with a step id: run")
	}
	for name, input := range action.Inputs {
		if input.Description == "" {
			t.Errorf("input %s has no description", name)
		}
		if name == "go-version" {
			continue // Consumed by setup-go, not the adapter.
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
