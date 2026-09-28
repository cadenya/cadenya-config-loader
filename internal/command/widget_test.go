package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWidgets(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	put(t, root, ".cadenya/widgets/support.yaml", "spec:\n  agentId: external_id:assistant\n  variationId: external_id:assistant/default\n  originAllowlist: [https://app.example.com]\n")
	out, _, err := invoke(t, f, root, "apply")
	if err != nil {
		t.Fatal(err)
	}
	m := mutations(f)
	last := m[len(m)-1]
	if !strings.HasSuffix(last.Path, "/widgets") || last.Method != "POST" {
		t.Fatalf("widgets are created last: %+v", last)
	}
	agentID, _ := find(f, "agents")
	variationID, _ := find(f, "variations")
	widgetID, _ := find(f, "widgets")
	spec := last.Body["spec"].(map[string]any)
	if spec["agentId"] != agentID || spec["variationId"] != variationID {
		t.Fatalf("widget references not resolved: %v", spec)
	}
	if !strings.Contains(out, `"host":"`+widgetID+`.widgets.test"`) {
		t.Fatalf("report is missing the embed host: %s", out)
	}

	// Re-point the widget and delete its old agent in one apply. The API refuses
	// to delete a bound agent, so the widget update has to come first.
	if err := os.RemoveAll(filepath.Join(root, ".cadenya/agents")); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".cadenya/agents/helper.yaml", "spec: {}")
	put(t, root, ".cadenya/agents/helper/default.yaml", "spec: {modelConfig: {modelId: getting-started-key.openai-gpt-4-1}}")
	put(t, root, ".cadenya/widgets/support.yaml", "spec: {agentId: 'external_id:helper', variationId: null}")
	f.resetRequests()
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	updated, deleted := -1, -1
	for i, r := range mutations(f) {
		if r.Method == "PATCH" && strings.HasSuffix(r.Path, widgetID) {
			updated = i
		}
		if r.Method == "DELETE" && strings.HasSuffix(r.Path, agentID) {
			deleted = i
		}
	}
	if updated < 0 || deleted < updated {
		t.Fatalf("widget must be re-pointed before its old agent is deleted: %+v", mutations(f))
	}
	if f.objects()[widgetID].Body["spec"].(map[string]any)["variationId"] != nil {
		t.Fatal("null did not unpin the variation")
	}

	// Pin a variation, then delete it without saying what the widget pins now.
	put(t, root, ".cadenya/widgets/support.yaml", "spec: {agentId: 'external_id:helper', variationId: 'external_id:helper/default'}")
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".cadenya/agents/helper/default.yaml")); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".cadenya/agents/helper/v2.yaml", "spec: {modelConfig: {modelId: getting-started-key.openai-gpt-4-1}}")
	put(t, root, ".cadenya/widgets/support.yaml", "spec: {agentId: 'external_id:helper'}")
	if _, _, err := invoke(t, f, root, "plan"); err == nil || !strings.Contains(err.Error(), "set spec.variationId") {
		t.Fatalf("expected a request to set spec.variationId, got %v", err)
	}
}

func TestAgentBoundToForeignWidgetIsKept(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	f.seed("agents", "", "stale", "stale", "test", "STATE_DRAFT", map[string]any{})
	f.seed("widgets", "", "foreign-widget", "foreign", "other", "STATE_ACTIVE", map[string]any{"agentId": "stale"})
	_, _, err := invoke(t, f, root, "plan")
	if err == nil || !strings.Contains(err.Error(), "a widget outside bundle") {
		t.Fatalf("expected refusal, got %v", err)
	}
}
