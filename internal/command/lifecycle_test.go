package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func memoryBundle(t *testing.T) string {
	root := localBundle(t)
	put(t, root, ".cadenya/memoryLayers/skills.yaml", "spec: {type: MEMORY_LAYER_TYPE_SKILLS, description: How we work}")
	put(t, root, ".cadenya/memoryLayers/skills/postmortem.yaml", "spec: {key: skills/postmortem, content: v1}")
	put(t, root, ".cadenya/agents/assistant/default.yaml", "spec:\n  modelConfig: {modelId: getting-started-key.openai-gpt-4-1}\n  memoryLayerAssignments:\n    - {memoryLayerId: 'external_id:skills', position: 0}\n")
	return root
}

func find(f *fixture, collection string) (string, object) {
	for id, v := range f.objects() {
		if v.Collection == collection {
			return id, v
		}
	}
	return "", object{}
}

func TestMemoryLayersEndToEnd(t *testing.T) {
	f := newFixture(t)
	root := memoryBundle(t)
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, m := range mutations(f) {
		order = append(order, m.Path[strings.LastIndex(m.Path, "/")+1:])
	}
	if strings.Join(order, ",") != "tool_sets,tools,memory_layers,entries,agents,variations" {
		t.Fatalf("dependency order: %v", order)
	}
	layerID, _ := find(f, "memory_layers")
	_, variation := find(f, "variations")
	layers := variation.Body["spec"].(map[string]any)["memoryLayerAssignments"].([]any)
	if layers[0].(map[string]any)["memoryLayerId"] != layerID {
		t.Fatalf("memory layer reference not resolved: %v", layers)
	}
	if _, entry := find(f, "entries"); entry.Body["content"] != "v1" || entry.Body["spec"].(map[string]any)["key"] != "skills/postmortem" {
		t.Fatalf("entry not created from content: %v", entry.Body)
	}

	put(t, root, ".cadenya/memoryLayers/skills/postmortem.yaml", "spec: {key: skills/postmortem, content: v2}")
	f.resetRequests()
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	for _, m := range mutations(f) {
		if strings.Contains(m.Path, "/entries/") && m.Body["updateMask"] != "content,key" {
			t.Fatalf("entry mask must name bare spec fields: %v", m.Body["updateMask"])
		}
	}
	if _, entry := find(f, "entries"); entry.Body["content"] != "v2" {
		t.Fatal("entry content not updated")
	}

	// Retiring the layer requires the variation to say what replaces it.
	if err := os.RemoveAll(filepath.Join(root, ".cadenya/memoryLayers")); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".cadenya/agents/assistant/default.yaml", "spec: {modelConfig: {modelId: getting-started-key.openai-gpt-4-1}}")
	if _, _, err := invoke(t, f, root, "plan"); err == nil || !strings.Contains(err.Error(), "include spec.memoryLayerAssignments") {
		t.Fatalf("expected a request for spec.memoryLayerAssignments, got %v", err)
	}
	put(t, root, ".cadenya/agents/assistant/default.yaml", "spec: {memoryLayerAssignments: []}")
	f.resetRequests()
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	m := mutations(f)
	if m[0].Method != "PATCH" || m[0].Body["updateMask"] != "spec.memoryLayerAssignments" || m[1].Method != "DELETE" || !strings.Contains(m[1].Path, "/entries/") || m[2].Method != "DELETE" || !strings.HasSuffix(m[2].Path, layerID) {
		t.Fatalf("expected detach, entry delete, layer delete: %+v", m[:3])
	}
}

func TestAgentState(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	put(t, root, ".cadenya/agents/assistant.yaml", "state: published\nspec: {description: ''}")
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	m := mutations(f)
	if last := m[len(m)-1]; !strings.HasSuffix(last.Path, ":publish") || !strings.HasSuffix(m[len(m)-2].Path, "/variations") {
		t.Fatalf("publish must follow the variation create: %+v", m)
	}
	agentID, agent := find(f, "agents")
	if agent.Body["state"] != "STATE_PUBLISHED" {
		t.Fatal("agent not published")
	}

	out, _, err := invoke(t, f, root, "plan")
	if err != nil || !strings.Contains(out, `"stateChanges":0`) {
		t.Fatalf("a published agent should not be published again: %v %s", err, out)
	}

	// Replacing the only variation of a published agent: the API refuses to
	// delete it until the replacement exists.
	if err := os.Remove(filepath.Join(root, ".cadenya/agents/assistant/default.yaml")); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".cadenya/agents/assistant/v2.yaml", "spec: {modelConfig: {modelId: getting-started-key.openai-gpt-4-1}}")
	f.resetRequests()
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	m = mutations(f)
	if last := m[len(m)-1]; last.Method != "DELETE" || !strings.Contains(last.Path, "/variations/") {
		t.Fatalf("variation of a surviving agent must be deleted last: %+v", m)
	}

	put(t, root, ".cadenya/agents/assistant.yaml", "state: draft\nspec: {description: ''}")
	f.resetRequests()
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	if m = mutations(f); !strings.HasSuffix(m[0].Path, agentID+":unpublish") {
		t.Fatalf("draft must unpublish: %+v", m)
	}

	// Deleting a published agent unpublishes it before its last variation goes.
	put(t, root, ".cadenya/agents/assistant.yaml", "state: published\nspec: {description: ''}")
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".cadenya/agents")); err != nil {
		t.Fatal(err)
	}
	f.resetRequests()
	out, _, err = invoke(t, f, root, "apply")
	if err != nil {
		t.Fatal(err)
	}
	m = mutations(f)
	if !strings.HasSuffix(m[0].Path, ":unpublish") || m[1].Method != "DELETE" || m[2].Method != "DELETE" || !strings.Contains(out, `"stateChanges":1`) {
		t.Fatalf("expected unpublish, then deletes: %+v", m)
	}
}

func TestArchivedAgentWithStateIsUnarchived(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	f.seed("agents", "", "archived", "assistant", "test", "STATE_ARCHIVED", map[string]any{})
	put(t, root, ".cadenya/agents/assistant.yaml", "state: draft\nspec: {description: ''}")
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	if m := mutations(f); !strings.HasSuffix(m[0].Path, "archived:unarchive") {
		t.Fatalf("expected unarchive first: %+v", m)
	}
	if f.objects()["archived"].Body["state"] != "STATE_DRAFT" {
		t.Fatal("agent still archived")
	}
}
