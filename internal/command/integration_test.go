package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type object struct {
	Collection, Parent string
	Body               map[string]any
}
type request struct {
	Method, Path string
	Query        map[string][]string
	Body         map[string]any
}
type fixture struct {
	t        *testing.T
	mu       sync.Mutex
	items    map[string]object
	requests []request
	serial   int
	fail     func(*http.Request) bool
	server   *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	// A developer's direnv settings must never change the fixture's ownership
	// scope or workspace. Tests that exercise precedence set their own values.
	t.Setenv("CADENYA_BUNDLE_KEY", "test")
	t.Setenv("CADENYA_WORKSPACE_ID", "development")
	f := &fixture{t: t, items: map[string]object{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) seed(collection, parent, id, external, bundle, state string, spec map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[id] = object{collection, parent, map[string]any{"metadata": map[string]any{"id": id, "externalId": external, "name": external, "labels": map[string]any{"bundle_key": bundle}}, "spec": spec, "state": state}}
}

func bare() map[string]any {
	return map[string]any{"adapter": map[string]any{"type": "bare", "bare": map[string]any{}}}
}
func toolSpec() map[string]any {
	return map[string]any{"parameters": map[string]any{}, "config": map[string]any{"type": "bare", "bare": map[string]any{}}}
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer test-key" {
		f.t.Errorf("incorrect API key header")
	}
	var body map[string]any
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		if len(data) > 0 {
			if err := json.Unmarshal(data, &body); err != nil {
				f.t.Error(err)
			}
		}
	}
	f.requests = append(f.requests, request{r.Method, r.URL.Path, r.URL.Query(), body})
	if f.fail != nil && f.fail(r) {
		w.WriteHeader(400)
		io.WriteString(w, `{"code":3,"message":"fixture failure"}`)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "workspaces" {
		f.t.Errorf("bad API path %s", r.URL.Path)
		w.WriteHeader(404)
		return
	}
	collection, parent, id := parts[3], "", ""
	if len(parts) == 5 {
		id = parts[4]
	}
	if len(parts) >= 6 {
		parent, collection = parts[4], parts[5]
		if len(parts) == 7 {
			id = parts[6]
		}
	}
	action := ""
	for _, a := range []string{"publish", "unpublish", "unarchive"} {
		if strings.HasSuffix(id, ":"+a) {
			id, action = strings.TrimSuffix(id, ":"+a), a
		}
	}
	if strings.HasPrefix(id, "external_id:") {
		want := strings.TrimPrefix(id, "external_id:")
		id = "missing"
		for key, v := range f.items {
			if v.Collection == collection && v.Parent == parent && v.Body["metadata"].(map[string]any)["externalId"] == want {
				id = key
				break
			}
		}
	}
	if r.Method == "GET" && id == "" {
		var ids []string
		for key, v := range f.items {
			if v.Collection != collection || v.Parent != parent {
				continue
			}
			if label := r.URL.Query().Get("labels"); label != "" {
				if label != "bundle_key=test" {
					f.t.Errorf("incorrect selector %q", label)
				}
				if v.Body["metadata"].(map[string]any)["labels"].(map[string]any)["bundle_key"] != "test" {
					continue
				}
			}
			if state := r.URL.Query().Get("state"); state != "" && v.Body["state"] != state {
				continue
			}
			if states := r.URL.Query()["states"]; len(states) > 0 && !contains(states, fmt.Sprint(v.Body["state"])) {
				continue
			}
			if agent := r.URL.Query().Get("agentId"); agent != "" && v.Body["spec"].(map[string]any)["agentId"] != agent {
				continue
			}
			ids = append(ids, key)
		}
		sort.Strings(ids)
		start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		end := start + 1 // One item per page deliberately exercises every SDK paginator.
		if end > len(ids) {
			end = len(ids)
		}
		items := []any{}
		for _, key := range ids[start:end] {
			items = append(items, f.items[key].Body)
		}
		next := ""
		if end < len(ids) {
			next = strconv.Itoa(end)
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items, "pagination": map[string]any{"nextCursor": next}})
		return
	}
	v, exists := f.items[id]
	if id != "" && !exists {
		w.WriteHeader(404)
		io.WriteString(w, `{"code":5,"message":"not found"}`)
		return
	}
	if action != "" {
		// Mirrors the API: an agent needs a variation to be published.
		if action == "publish" && f.children(id, "variations") == 0 {
			w.WriteHeader(400)
			io.WriteString(w, `{"code":3,"message":"agents must have active variations to be published"}`)
			return
		}
		v.Body["state"] = map[string]string{"publish": "STATE_PUBLISHED", "unpublish": "STATE_DRAFT", "unarchive": "STATE_DRAFT"}[action]
		f.items[id] = v
		json.NewEncoder(w).Encode(v.Body)
		return
	}
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(v.Body)
	case "POST":
		if collection == "entries" {
			// Entries are created from a content union; reads return key and
			// description in spec and the body separately.
			spec := body["spec"].(map[string]any)
			body["content"] = spec["content"]
			body["spec"] = map[string]any{"key": spec["key"], "description": spec["description"]}
		}
		f.serial++
		id = fmt.Sprintf("created_%s_%d", collection, f.serial)
		body["metadata"].(map[string]any)["id"] = id
		state := "STATE_DRAFT"
		if collection == "tool_sets" {
			state = "STATE_ACTIVE"
		}
		if collection == "tools" {
			state = "STATE_AVAILABLE"
		}
		if collection == "widgets" {
			state = "STATE_ACTIVE"
			body["info"] = map[string]any{"host": id + ".widgets.test"}
		}
		body["state"] = state
		f.items[id] = object{collection, parent, body}
		json.NewEncoder(w).Encode(body)
	case "PATCH":
		mask, ok := body["updateMask"].(string)
		if !ok || mask == "" {
			f.t.Error("update must include a field mask")
		}
		if collection == "entries" {
			// Entry masks name spec fields without a prefix. Metadata always
			// replaces the stored metadata.
			for _, field := range strings.Split(mask, ",") {
				src, _ := body["spec"].(map[string]any)
				switch field {
				case "key", "description":
					v.Body["spec"].(map[string]any)[field] = src[field]
				case "content":
					v.Body["content"] = src[field]
				default:
					w.WriteHeader(400)
					io.WriteString(w, `{"code":3,"message":"invalid field mask"}`)
					return
				}
			}
			md := body["metadata"].(map[string]any)
			md["id"] = id
			v.Body["metadata"] = md
			f.items[id] = v
			json.NewEncoder(w).Encode(v.Body)
			return
		}
		for _, field := range strings.Split(mask, ",") {
			path := strings.SplitN(field, ".", 2)
			if len(path) != 2 {
				f.t.Errorf("unexpected mask %q", field)
				continue
			}
			if v.Body[path[0]] == nil {
				v.Body[path[0]] = map[string]any{}
			}
			dst := v.Body[path[0]].(map[string]any)
			src, _ := body[path[0]].(map[string]any)
			if value, ok := src[path[1]]; ok {
				dst[path[1]] = value
			} else {
				delete(dst, path[1])
			}
		}
		f.items[id] = v
		json.NewEncoder(w).Encode(v.Body)
	case "DELETE":
		if collection == "variations" {
			for _, other := range f.items {
				if other.Collection == "widgets" && other.Body["spec"].(map[string]any)["variationId"] == id {
					w.WriteHeader(400)
					io.WriteString(w, `{"code":3,"message":"variation is pinned by a widget"}`)
					return
				}
			}
		}
		if collection == "variations" && f.items[parent].Body["state"] == "STATE_PUBLISHED" && f.children(parent, "variations") == 1 {
			w.WriteHeader(400)
			io.WriteString(w, `{"code":9,"message":"cannot delete the last variation of a published agent"}`)
			return
		}
		if collection == "agents" {
			for _, other := range f.items {
				if other.Collection == "widgets" && other.Body["spec"].(map[string]any)["agentId"] == id {
					w.WriteHeader(400)
					io.WriteString(w, `{"code":3,"message":"agent cannot be deleted while a widget is bound to it"}`)
					return
				}
			}
		}
		if collection == "memory_layers" {
			for _, other := range f.items {
				spec, _ := other.Body["spec"].(map[string]any)
				layers, _ := spec["memoryLayerAssignments"].([]any)
				for _, a := range layers {
					if a.(map[string]any)["memoryLayerId"] == id {
						w.WriteHeader(400)
						io.WriteString(w, `{"code":3,"message":"memory layer cannot be deleted while it is assigned to agent variations"}`)
						return
					}
				}
			}
		}
		for _, child := range f.items {
			if child.Parent == id {
				f.t.Errorf("parent deleted before child: %s", id)
			}
		}
		delete(f.items, id)
		w.WriteHeader(204)
	default:
		f.t.Errorf("unexpected method %s", r.Method)
		w.WriteHeader(400)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// children counts stored children of parent in collection. Callers hold f.mu.
func (f *fixture) children(parent, collection string) int {
	n := 0
	for _, v := range f.items {
		if v.Parent == parent && v.Collection == collection {
			n++
		}
	}
	return n
}

func put(t *testing.T, root, path, body string) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func localBundle(t *testing.T) string {
	root := t.TempDir()
	put(t, root, "cadenya.yaml", "bundleKey: test\nworkspaceId: development\n")
	put(t, root, ".cadenya/toolSets/ui.yaml", "externalId: frontend\nspec: {adapter: {type: bare, bare: {}}}")
	put(t, root, ".cadenya/toolSets/ui/fetch.yaml", "spec: {parameters: {}, config: {type: bare, bare: {}}}")
	put(t, root, ".cadenya/agents/assistant.yaml", "spec: {description: ''}")
	put(t, root, ".cadenya/agents/assistant/default.yaml", "spec:\n  modelConfig: {modelId: getting-started-key.openai-gpt-4-1, temperature: 0}\n  assignments:\n    - {type: toolId, toolId: 'external_id:frontend/fetch'}\n    - {type: toolSetId, toolSetId: 'external_id:frontend'}\n")
	return root
}

func invoke(t *testing.T, f *fixture, root string, args ...string) (string, string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	base := []string{"cadenya-config", "--directory", root, "--base-url", f.server.URL, "--api-key", "test-key", "--output", "json"}
	err := New("test", &out, &stderr).Run(context.Background(), append(base, args...))
	return out.String(), stderr.String(), err
}

func (f *fixture) setFailure(fail func(*http.Request) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}
func (f *fixture) resetRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}
func (f *fixture) calls() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}
func (f *fixture) objects() map[string]object {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := json.Marshal(f.items)
	if err != nil {
		f.t.Fatal(err)
	}
	var result map[string]object
	if err := json.Unmarshal(data, &result); err != nil {
		f.t.Fatal(err)
	}
	return result
}

func mutations(f *fixture) []request {
	var result []request
	for _, r := range f.calls() {
		if r.Method != "GET" {
			result = append(result, r)
		}
	}
	return result
}

func TestApplyEndToEnd(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	// Include two pages at every hierarchy level, archived parents, and resources
	// from another bundle. All managed stale children must precede their parents.
	for i := 1; i <= 2; i++ {
		suffix := strconv.Itoa(i)
		f.seed("tool_sets", "", "old-set"+suffix, "old-set"+suffix, "test", "STATE_ARCHIVED", bare())
		f.seed("agents", "", "old-agent"+suffix, "old-agent"+suffix, "test", "STATE_ARCHIVED", map[string]any{})
		for j := 1; j <= 2; j++ {
			s := suffix + strconv.Itoa(j)
			f.seed("tools", "old-set"+suffix, "old-tool"+s, "tool"+strconv.Itoa(j), "test", "STATE_OMITTED", toolSpec())
			f.seed("variations", "old-agent"+suffix, "old-var"+s, "variation"+strconv.Itoa(j), "test", "", map[string]any{})
		}
	}
	f.seed("agents", "", "unmanaged", "unmanaged", "other", "STATE_DRAFT", map[string]any{})
	out, _, err := invoke(t, f, root, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(mutations(f)) != 0 {
		t.Fatal("plan mutated the API")
	}
	var plan struct {
		Applied bool
		Summary struct{ Creates, Updates, Deletes int }
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Summary.Creates != 4 || plan.Summary.Deletes != 12 {
		t.Fatalf("unexpected plan %s", out)
	}
	seenPage := map[string]bool{}
	for _, r := range f.calls() {
		if len(r.Query["cursor"]) > 0 {
			seenPage[r.Path] = true
			if len(r.Query["labels"]) > 0 && r.Query["labels"][0] != "bundle_key=test" {
				t.Fatal("pagination lost selector")
			}
		}
	}
	if len(seenPage) < 6 {
		t.Fatalf("pagination missing: %v", seenPage)
	}
	f.resetRequests()
	out, _, err = invoke(t, f, root, "apply")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"applied":true`) {
		t.Fatal(out)
	}
	m := mutations(f)
	if len(m) != 16 {
		t.Fatalf("got %d writes", len(m))
	}
	for i := 0; i < 12; i++ {
		if m[i].Method != "DELETE" {
			t.Fatalf("deletes must precede writes: %+v", m)
		}
	}
	for i, want := range []string{"/tool_sets", "/tools", "/agents", "/variations"} {
		if !strings.HasSuffix(m[12+i].Path, want) || m[12+i].Method != "POST" {
			t.Fatalf("dependency order: %+v", m[12+i])
		}
	}
	if _, ok := f.objects()["unmanaged"]; !ok {
		t.Fatal("removed another bundle")
	}
	var toolID, setID string
	for id, v := range f.objects() {
		if v.Collection == "tools" {
			toolID = id
		}
		if v.Collection == "tool_sets" {
			setID = id
		}
	}
	assignments := m[15].Body["spec"].(map[string]any)["assignments"].([]any)
	if assignments[0].(map[string]any)["toolId"] != toolID || assignments[1].(map[string]any)["toolSetId"] != setID {
		t.Fatal("local references were not resolved to created IDs")
	}
	f.resetRequests()
	put(t, root, ".cadenya/agents/assistant/default.yaml", "spec: {assignments: [], systemPromptTemplate: ''}")
	out, _, err = invoke(t, f, root, "apply")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"updates":4`) {
		t.Fatal(out)
	}
	for _, r := range mutations(f) {
		if r.Method != "PATCH" {
			t.Fatalf("repeat apply created duplicate: %+v", r)
		}
	}
	for _, v := range f.objects() {
		if v.Collection == "variations" {
			assignments, _ := v.Body["spec"].(map[string]any)["assignments"].([]any)
			if len(assignments) != 0 {
				t.Fatal("assignments not cleared")
			}
		}
	}
}

func TestDetachBeforeDeletingAssignedTools(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".cadenya/toolSets/ui/fetch.yaml")); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".cadenya/agents/assistant/default.yaml", "spec: {assignments: []}")
	f.resetRequests()
	if _, _, err := invoke(t, f, root, "apply"); err != nil {
		t.Fatal(err)
	}
	m := mutations(f)
	if m[0].Method != "PATCH" || m[0].Body["updateMask"] != "spec.assignments" || m[1].Method != "DELETE" {
		t.Fatalf("expected detach then delete: %+v", m)
	}
}

func TestPreflightFailuresNeverMutate(t *testing.T) {
	for _, scenario := range []string{"invalid YAML", "foreign identity", "foreign child", "foreign tool", "pagination error", "duplicate remote identity", "mislabeled response"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			root := localBundle(t)
			f.seed("agents", "", "stale", "stale", "test", "STATE_DRAFT", map[string]any{})
			switch scenario {
			case "invalid YAML":
				put(t, root, ".cadenya/agents/broken.yaml", "spec: {oops: value}")
			case "foreign identity":
				f.seed("agents", "", "foreign", "assistant", "other", "STATE_DRAFT", map[string]any{})
			case "foreign child":
				f.seed("variations", "stale", "foreign-var", "default", "other", "", map[string]any{})
			case "foreign tool":
				f.seed("tool_sets", "", "stale-set", "stale-set", "test", "STATE_ACTIVE", bare())
				f.seed("tools", "stale-set", "foreign-tool", "fetch", "other", "STATE_AVAILABLE", toolSpec())
			case "pagination error":
				f.seed("agents", "", "stale2", "stale2", "test", "STATE_DRAFT", map[string]any{})
				f.setFailure(func(r *http.Request) bool { return r.URL.Query().Get("cursor") != "" })
			case "duplicate remote identity":
				f.seed("agents", "", "duplicate", "stale", "test", "STATE_DRAFT", map[string]any{})
			case "mislabeled response":
				// A broken selector must not silently grant ownership; inject a wrong
				// label after filtering by using a dedicated response server below.
				bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.WriteString(w, `{"items":[{"metadata":{"id":"x","externalId":"x","labels":{"bundle_key":"other"}},"spec":{}}]}`)
				}))
				defer bad.Close()
				f.server = bad
			}
			_, _, err := invoke(t, f, root, "apply")
			if err == nil {
				t.Fatal("expected failure")
			}
			if len(mutations(f)) != 0 {
				t.Fatalf("mutated before failing: %+v", mutations(f))
			}
		})
	}
}

func TestFailureReportsPartialProgressAndStops(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	f.setFailure(func(r *http.Request) bool { return r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/tools") })
	out, _, err := invoke(t, f, root, "apply")
	if err == nil {
		t.Fatal("expected apply failure")
	}
	var result struct {
		Applied    bool
		Operations []struct{ Completed bool }
		Error      string
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Error == "" || !result.Operations[0].Completed || result.Operations[1].Completed {
		t.Fatal(out)
	}
	if len(mutations(f)) != 2 {
		t.Fatalf("must stop immediately after failure: %+v", mutations(f))
	}
}

func TestFlagsEnvironmentConfigAndEmptyGuard(t *testing.T) {
	f := newFixture(t)
	root := localBundle(t)
	t.Setenv("CADENYA_API_KEY", "test-key")
	t.Setenv("CADENYA_BASE_URL", f.server.URL)
	t.Setenv("CADENYA_WORKSPACE_ID", "env-workspace")
	t.Setenv("CADENYA_BUNDLE_KEY", "test")
	var out bytes.Buffer
	if err := New("test", &out, io.Discard).Run(context.Background(), []string{"cadenya-config", "plan", "-C", root, "--workspace-id", "flag-workspace"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.calls() {
		if !strings.Contains(r.Path, "/workspaces/flag-workspace/") {
			t.Fatal(r.Path)
		}
	}
	f.resetRequests()
	if err := New("test", io.Discard, io.Discard).Run(context.Background(), []string{"cadenya-config", "-C", root, "plan"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.calls() {
		if !strings.Contains(r.Path, "/workspaces/env-workspace/") {
			t.Fatal(r.Path)
		}
	}
	if err := os.RemoveAll(filepath.Join(root, ".cadenya")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".cadenya"), 0755); err != nil {
		t.Fatal(err)
	}
	f.resetRequests()
	if _, _, err := invoke(t, f, root, "apply"); err == nil || !strings.Contains(err.Error(), "allow-empty") {
		t.Fatalf("empty guard: %v", err)
	}
	if len(f.calls()) != 0 {
		t.Fatal("empty guard must run before API reads")
	}
	f.seed("agents", "", "stale", "stale", "test", "STATE_DRAFT", map[string]any{})
	if _, _, err := invoke(t, f, root, "apply", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if len(mutations(f)) != 0 {
		t.Fatal("dry run wrote to API")
	}
	if _, _, err := invoke(t, f, root, "apply", "--allow-empty"); err != nil {
		t.Fatal(err)
	}
	if len(f.objects()) != 0 {
		t.Fatal("allow-empty did not prune")
	}
}

func TestValidateIsOfflineAndSettingsOptional(t *testing.T) {
	root := localBundle(t)
	if err := os.Remove(filepath.Join(root, "cadenya.yaml")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CADENYA_API_KEY", "")
	var out bytes.Buffer
	if err := New("test", &out, io.Discard).Run(context.Background(), []string{"cadenya-config", "validate", "-C", root, "--bundle-key", "test", "--output", "json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"valid":true`) {
		t.Fatal(out.String())
	}
}
