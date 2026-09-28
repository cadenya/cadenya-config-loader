package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for release-review findings. Each scenario used to pass plan
// and then fail partway through apply.

func TestOutsideVariationBlocksDeletingWhatItAssigns(t *testing.T) {
	f := newFixture(t)
	root := memoryBundle(t)
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	layerID, _ := find(f, "memory_layers")
	f.seed("agents", "", "hand-made", "hand-made", "", "STATE_DRAFT", map[string]any{})
	f.seed("variations", "hand-made", "hand-var", "default", "", "", map[string]any{"memoryLayerAssignments": []any{map[string]any{"memoryLayerId": layerID, "position": 0}}})
	if err := os.RemoveAll(filepath.Join(root, ".cadenya/memoryLayers")); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".cadenya/agents/assistant/default.yaml", "spec: {memoryLayerAssignments: []}")
	f.resetRequests()
	_, _, err := invoke(t, f, root, "apply")
	if err == nil || !strings.Contains(err.Error(), "variation hand-var of agent hand-made, outside bundle") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if len(mutations(f)) != 0 {
		t.Fatalf("plan must refuse before any write: %+v", mutations(f))
	}
}

func TestOutsideWidgetBlocksDeletingItsPinnedVariation(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	put(t, root, ".cadenya/agents/assistant/v2.yaml", "spec: {modelConfig: {modelId: getting-started-key.openai-gpt-4-1}}")
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	var v2 string
	for id, v := range f.objects() {
		if v.Collection == "variations" && v.Body["metadata"].(map[string]any)["externalId"] == "v2" {
			v2 = id
		}
	}
	agentID, _ := find(f, "agents")
	f.seed("widgets", "", "foreign-widget", "foreign", "other", "STATE_ACTIVE", map[string]any{"agentId": agentID, "variationId": v2})
	if err := os.Remove(filepath.Join(root, ".cadenya/agents/assistant/v2.yaml")); err != nil {
		t.Fatal(err)
	}
	_, _, err := invoke(t, f, root, "plan")
	if err == nil || !strings.Contains(err.Error(), "widget foreign-widget, outside bundle") {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestPublishedAgentWithoutStateKeepsAVariation(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	put(t, root, ".cadenya/agents/assistant.yaml", "state: published\nspec: {description: ''}")
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	// Drop the state line and every variation. The agent is still published.
	put(t, root, ".cadenya/agents/assistant.yaml", "spec: {description: ''}")
	if err := os.RemoveAll(filepath.Join(root, ".cadenya/agents/assistant")); err != nil {
		t.Fatal(err)
	}
	f.resetRequests()
	_, _, err := invoke(t, f, root, "apply")
	if err == nil || !strings.Contains(err.Error(), "every variation of a published agent") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if len(mutations(f)) != 0 {
		t.Fatal("plan must refuse before any write")
	}
	// Setting state: draft is the documented way out.
	put(t, root, ".cadenya/agents/assistant.yaml", "state: draft\nspec: {description: ''}")
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
}

func TestLateAgentIsUnpublishedAfterItsWidgetMoves(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	put(t, root, ".cadenya/agents/assistant.yaml", "state: published\nspec: {description: ''}")
	put(t, root, ".cadenya/widgets/support.yaml", "spec: {agentId: 'external_id:assistant'}")
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	widgetID, _ := find(f, "widgets")
	if err := os.RemoveAll(filepath.Join(root, ".cadenya/agents")); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".cadenya/agents/helper.yaml", "spec: {}")
	put(t, root, ".cadenya/agents/helper/default.yaml", "spec: {modelConfig: {modelId: getting-started-key.openai-gpt-4-1}}")
	put(t, root, ".cadenya/widgets/support.yaml", "spec: {agentId: 'external_id:helper'}")
	f.resetRequests()
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	moved, unpublished := -1, -1
	for i, r := range mutations(f) {
		if r.Method == "PATCH" && strings.HasSuffix(r.Path, widgetID) {
			moved = i
		}
		if strings.HasSuffix(r.Path, ":unpublish") {
			unpublished = i
		}
	}
	if moved < 0 || unpublished < moved {
		t.Fatalf("the old agent must stay published until the widget moves: %+v", mutations(f))
	}
}

func TestLogsNeverContainTheAPIKey(t *testing.T) {
	var buf bytes.Buffer
	log, err := newLogger("debug", "json", "sk-secret", &buf)
	if err != nil {
		t.Fatal(err)
	}
	log = log.With("context", "carries sk-secret")
	log.Error("request with sk-secret failed", "error", errors.New("bad key sk-secret"), "detail", "sk-secret")
	if strings.Contains(buf.String(), "sk-secret") || !strings.Contains(buf.String(), "[REDACTED]") {
		t.Fatalf("secret reached the log: %s", buf.String())
	}
}
