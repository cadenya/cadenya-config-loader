//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cadenya "go.cadenya.com/cadenya-go"
)

// TestLiveBundle is deliberately excluded from normal CI. It only manages a
// random bundle with unique parent external IDs inside an explicitly selected
// disposable workspace. No objectives are run and no agents are published.
func TestLiveBundle(t *testing.T) {
	if os.Getenv("CADENYA_LIVE_TEST") != "1" {
		t.Skip("set CADENYA_LIVE_TEST=1 to opt in")
	}
	workspace, key, model := os.Getenv("CADENYA_WORKSPACE_ID"), os.Getenv("CADENYA_API_KEY"), os.Getenv("CADENYA_TEST_MODEL_ID")
	if workspace == "" || key == "" || model == "" {
		t.Fatal("set CADENYA_WORKSPACE_ID, CADENYA_API_KEY, and CADENYA_TEST_MODEL_ID for a disposable workspace")
	}
	base := strings.TrimSpace(os.Getenv("CADENYA_BASE_URL"))
	if base == "" {
		base = "https://api.cadenya.com"
	}
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	bundle := "cli-acceptance-" + hex.EncodeToString(entropy[:])
	root := t.TempDir()
	put(t, root, ".cadenya/toolSets/ui.yaml", "externalId: "+bundle+"-tools\nspec: {adapter: {type: bare, bare: {}}}")
	put(t, root, ".cadenya/toolSets/ui/fetch.yaml", "spec: {description: Fetch the current form, parameters: {}, config: {type: bare, bare: {}}}")
	put(t, root, ".cadenya/memoryLayers/skills.yaml", "externalId: "+bundle+"-skills\nspec: {type: MEMORY_LAYER_TYPE_SKILLS, description: Acceptance test}")
	put(t, root, ".cadenya/memoryLayers/skills/howto.yaml", "spec: {key: skills/howto, description: How to test, content: v1}")
	put(t, root, ".cadenya/agents/assistant.yaml", "externalId: "+bundle+"-agent\nstate: published\nspec: {description: Acceptance test}")
	// JSON is also valid YAML and safely quotes a workspace's model identifier.
	variation, _ := json.Marshal(map[string]any{"spec": map[string]any{"systemPromptTemplate": "Help with the current form.", "modelConfig": map[string]any{"modelId": model}, "assignments": []any{map[string]any{"type": "toolSetId", "toolSetId": "external_id:" + bundle + "-tools"}}, "memoryLayerAssignments": []any{map[string]any{"memoryLayerId": "external_id:" + bundle + "-skills", "position": 0}}}})
	put(t, root, ".cadenya/agents/assistant/default.yaml", string(variation))
	put(t, root, ".cadenya/widgets/support.yaml", "externalId: "+bundle+"-widget\nspec: {agentId: 'external_id:"+bundle+"-agent', variationId: 'external_id:"+bundle+"-agent/default', originAllowlist: ['http://localhost:3000']}")
	run := func(mode string, extra ...string) (Report, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var out bytes.Buffer
		args := []string{"cadenya-config", mode, "-C", root, "--bundle-key", bundle, "--workspace-id", workspace, "--api-key", key, "--base-url", base, "--output", "json"}
		err := New("live-test", &out, io.Discard).Run(ctx, append(args, extra...))
		var result Report
		if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil && err == nil {
			return result, decodeErr
		}
		return result, err
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(root, ".cadenya")); err != nil {
			t.Error(err)
			return
		}
		if err := os.Mkdir(filepath.Join(root, ".cadenya"), 0755); err != nil {
			t.Error(err)
			return
		}
		if _, err := run("apply", "--allow-empty"); err != nil {
			t.Errorf("cleanup of bundle %s failed: %v", bundle, err)
		}
	})
	created, err := run("apply")
	if err != nil {
		t.Fatal(err)
	}
	if !created.Applied || created.Summary.Creates != 7 || created.Summary.StateChanges != 1 {
		t.Fatalf("unexpected create result: %+v", created)
	}
	put(t, root, ".cadenya/agents/assistant/default.yaml", "spec: {assignments: []}")
	if _, err := run("apply"); err != nil {
		t.Fatal(err)
	}
	client, err := cadenya.NewClient(cadenya.WithAPIKey(key), cadenya.WithBaseURL(base), cadenya.WithWorkspaceID(workspace))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	actual, err := client.Agents().Variations().Retrieve(ctx, "external_id:"+bundle+"-agent", "external_id:default", nil)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Spec == nil || len(actual.Spec.Assignments) != 0 {
		t.Fatal("API did not clear assignments using the update mask")
	}
	if len(actual.Spec.MemoryLayerAssignments) != 1 {
		t.Fatal("clearing assignments must not touch memory layer assignments")
	}
	var host string
	for _, op := range created.Operations {
		if op.Kind == "widget" {
			host = op.Host
		}
	}
	if !strings.HasSuffix(host, ".widgets.cadenya.com") {
		t.Fatalf("widget embed host %q", host)
	}
	agent, err := client.Agents().Retrieve(ctx, "external_id:"+bundle+"-agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if agent.State != "STATE_PUBLISHED" {
		t.Fatalf("agent state %s, want published", agent.State)
	}
	put(t, root, ".cadenya/memoryLayers/skills/howto.yaml", "spec: {key: skills/howto, content: v2}")
	if _, err := run("apply"); err != nil {
		t.Fatal(err)
	}
	entry, err := client.MemoryLayers().Entries().Retrieve(ctx, "external_id:"+bundle+"-skills", "external_id:howto", nil)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Content != "v2" || entry.Spec.Description != "How to test" {
		t.Fatalf("entry update: content %q description %q", entry.Content, entry.Spec.Description)
	}
	if err := os.Remove(filepath.Join(root, ".cadenya/toolSets/ui/fetch.yaml")); err != nil {
		t.Fatal(err)
	}
	pruned, err := run("apply")
	if err != nil {
		t.Fatal(err)
	}
	if pruned.Summary.Deletes != 1 {
		t.Fatalf("expected one pruned tool, got %+v", pruned.Summary)
	}
	// A layer can't be deleted while a variation assigns it, so apply detaches first.
	if err := os.RemoveAll(filepath.Join(root, ".cadenya/memoryLayers")); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".cadenya/agents/assistant/default.yaml", "spec: {assignments: [], memoryLayerAssignments: []}")
	retired, err := run("apply")
	if err != nil {
		t.Fatal(err)
	}
	if retired.Summary.Detaches != 1 || retired.Summary.Deletes != 2 {
		t.Fatalf("expected one detach and two deletes, got %+v", retired.Summary)
	}
}
