package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cadenya "go.cadenya.com/cadenya-go"
)

func TestHTTPAndOpenAPIKeepServerTemplates(t *testing.T) {
	t.Setenv("SERVICE_API_KEY", "must-not-appear-in-resource")
	root := t.TempDir()
	write(t, root, "toolSets/http.yaml", `metadata:
  labels: {cadenya.com/team: ''}
spec:
  adapter:
    type: http
    http:
      baseUrl: https://{{ pinned_parameters.tenant }}.example.com
      headers:
        Authorization: Bearer ${SERVICE_API_KEY}
`)
	write(t, root, "toolSets/http/fetch.yaml", `spec:
  parameters: {type: object, properties: {value: {default: null}}}
  config: {type: http, http: {requestMethod: GET, path: /forms}}
`)
	write(t, root, "toolSets/openapi.yaml", `spec:
  adapter:
    type: openapi
    openapi: {type: url, url: 'https://example.com/openapi.json'}
`)
	b, err := Load(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(b.Resources[Key{Kind: ToolSet, ExternalID: "http"}].Spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "${SERVICE_API_KEY}") || !strings.Contains(string(data), "{{ pinned_parameters.tenant }}") || strings.Contains(string(data), "must-not-appear") {
		t.Fatal(string(data))
	}
}

func write(t *testing.T, root, path, body string) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIdentityAndMasks(t *testing.T) {
	root := t.TempDir()
	write(t, root, "agents/filename.yaml", "externalId: explicit\nmetadata:\n  labels:\n    team: platform\nspec: {}\n")
	write(t, root, "agents/filename/default.yml", "spec:\n  systemPromptTemplate: ''\n  assignments: []\n  modelConfig:\n    modelId: getting-started-key.openai-gpt-4-1\n    temperature: 0\n")
	write(t, root, "agents/second.yaml", "spec: {}\n")
	write(t, root, "agents/second/default.yaml", "spec: {}\n")
	b, err := Load(root, "test-bundle")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Resources) != 4 {
		t.Fatalf("got %d resources", len(b.Resources))
	}
	r := b.Resources[Key{Variation, "explicit", "default"}]
	if r == nil {
		t.Fatal("directory must follow filename; identity must follow explicit externalId")
	}
	if !strings.Contains(r.Mask, "spec.assignments") || !strings.Contains(r.Mask, "spec.systemPromptTemplate") {
		t.Fatal(r.Mask)
	}
	spec := r.Spec.(*cadenya.AgentVariationSpec)
	if spec.ModelConfig.Temperature == nil || *spec.ModelConfig.Temperature != 0 {
		t.Fatal("zero temperature lost")
	}
	if r.Metadata.Name != "default" || r.Metadata.Labels[BundleLabel] != "test-bundle" {
		t.Fatal(r.Metadata)
	}
}

func TestRejectInvalidBundle(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"duplicate IDs", map[string]string{"agents/a.yaml": "externalId: same\nspec: {}", "agents/b.yaml": "externalId: same\nspec: {}"}, "duplicate agent"},
		{"duplicate stems", map[string]string{"agents/a.yaml": "externalId: first\nspec: {}", "agents/a.yml": "externalId: second\nspec: {}"}, "duplicate parent filename"},
		{"orphan", map[string]string{"agents/missing/default.yaml": "spec: {}"}, "no matching parent"},
		{"unknown root directory", map[string]string{"toolsets/a.yaml": "spec: {}"}, "unexpected directory"},
		{"unknown spec", map[string]string{"agents/a.yaml": "spec: {descripton: typo}"}, "unknown field"},
		{"unknown metadata", map[string]string{"agents/a.yaml": "metadata: {lables: {team: x}}\nspec: {}"}, "unknown field"},
		{"unknown union field", map[string]string{"toolSets/a.yaml": "spec: {adapter: {type: bare, bare: {}, baer: {}}}"}, "unknown field"},
		{"unknown union payload", map[string]string{"toolSets/a.yaml": "spec: {adapter: {type: http, http: {baseUlr: typo}}}"}, "unknown field"},
		{"missing union type", map[string]string{"toolSets/a.yaml": "spec: {adapter: {}}"}, "type"},
		{"missing spec", map[string]string{"agents/a.yaml": "metadata: {name: A}"}, "spec must be"},
		{"wrong field casing", map[string]string{"agents/a.yaml": "Metadata: {name: A}\nspec: {}"}, "unknown field"},
		{"null union element", map[string]string{"agents/a.yaml": "spec: {}", "agents/a/v.yaml": "spec: {assignments: [null]}"}, "must not be null"},
		{"null label", map[string]string{"agents/a.yaml": "metadata: {labels: {team: null}}\nspec: {}"}, "must not be null"},
		{"invalid label", map[string]string{"agents/a.yaml": "metadata: {labels: {team: has spaces}}\nspec: {}"}, "invalid value"},
		{"invalid label prefix", map[string]string{"agents/a.yaml": "metadata: {labels: {'BAD_DOMAIN/team': platform}}\nspec: {}"}, "invalid label key prefix"},
		{"blank model", map[string]string{"agents/a.yaml": "spec: {}", "agents/a/v.yaml": "spec: {modelConfig: {modelId: ''}}"}, "modelId is required"},
		{"dotted model slug", map[string]string{"agents/a.yaml": "spec: {}", "agents/a/v.yaml": "spec: {modelConfig: {modelId: openai.gpt-4.1}}"}, "one dot"},
		{"state on a tool set", map[string]string{"toolSets/a.yaml": "state: published\nspec: {adapter: {type: bare, bare: {}}}"}, "only supported on agents"},
		{"unknown state", map[string]string{"agents/a.yaml": "state: archived\nspec: {}"}, "state must be draft or published"},
		{"published without variation", map[string]string{"agents/a.yaml": "state: published\nspec: {}"}, "requires at least one variation"},
		{"episodic layer", map[string]string{"memoryLayers/m.yaml": "spec: {type: MEMORY_LAYER_TYPE_EPISODIC}"}, "created by the runtime"},
		{"layer without type", map[string]string{"memoryLayers/m.yaml": "spec: {description: x}"}, "spec.type must be"},
		{"reserved memory key", map[string]string{"memoryLayers/m.yaml": "spec: {type: MEMORY_LAYER_TYPE_SKILLS}", "memoryLayers/m/e.yaml": "spec: {key: system/prompt}"}, "reserved prefixes"},
		{"bad memory key", map[string]string{"memoryLayers/m.yaml": "spec: {type: MEMORY_LAYER_TYPE_SKILLS}", "memoryLayers/m/e.yaml": "spec: {key: 'has space'}"}, "may contain only"},
		{"unknown entry field", map[string]string{"memoryLayers/m.yaml": "spec: {type: MEMORY_LAYER_TYPE_SKILLS}", "memoryLayers/m/e.yaml": "spec: {body: x}"}, "unknown field"},
		{"missing local layer", map[string]string{"agents/a.yaml": "spec: {}", "agents/a/v.yaml": "spec: {memoryLayerAssignments: [{memoryLayerId: 'external_id:nope', position: 0}]}"}, "missing local memoryLayer nope"},
		{"widget origin with path", map[string]string{"widgets/w.yaml": "spec: {agentId: agent_1, originAllowlist: ['https://example.com/app']}"}, "exact origin"},
		{"widget wildcard origin", map[string]string{"widgets/w.yaml": "spec: {agentId: agent_1, originAllowlist: ['https://*.example.com']}"}, "exact origin"},
		{"widget without agent", map[string]string{"widgets/w.yaml": "spec: {originAllowlist: []}"}, "agentId is required"},
		{"widget missing local agent", map[string]string{"widgets/w.yaml": "spec: {agentId: 'external_id:nope'}"}, "missing local agent nope"},
		{"widget pins another agent's variation", map[string]string{"agents/a.yaml": "spec: {}", "agents/a/v.yaml": "spec: {}", "agents/b.yaml": "spec: {}", "widgets/w.yaml": "spec: {agentId: 'external_id:b', variationId: 'external_id:a/v'}"}, "must be a variation of spec.agentId"},
		{"widget variation without agent", map[string]string{"widgets/w.yaml": "spec: {agentId: agent_1, variationId: 'external_id:v'}"}, "external_id:<agent-external-id>/<variation-external-id>"},
		{"widget children", map[string]string{"widgets/w.yaml": "spec: {agentId: agent_1}", "widgets/w/x.yaml": "spec: {}"}, "have no children"},
		{"blank pool", map[string]string{"agents/a.yaml": "spec: {}", "agents/a/v.yaml": "spec: {assignments: [{type: agentPoolId, agentPoolId: ''}]}"}, "must not be blank"},
		{"empty YAML", map[string]string{"agents/a.yaml": ""}, "EOF"},
		{"extra document", map[string]string{"agents/a.yaml": "spec: {}\n---\nspec: {}"}, "exactly one"},
		{"duplicate keys", map[string]string{"agents/a.yaml": "spec: {}\nspec: {}"}, "duplicate key"},
		{"label conflict", map[string]string{"agents/a.yaml": "metadata: {labels: {bundle_key: elsewhere}}\nspec: {}"}, "conflicts"},
		{"identity conflict", map[string]string{"agents/a.yaml": "externalId: first\nmetadata: {externalId: second}\nspec: {}"}, "conflicts"},
		{"explicit blank", map[string]string{"agents/a.yaml": "externalId: ''\nspec: {}"}, "nonblank"},
		{"synced tools", map[string]string{"toolSets/a.yaml": "spec: {adapter: {type: mcp, mcp: {url: 'https://example.com'}}}", "toolSets/a/t.yaml": "spec: {parameters: {}, config: {type: bare, bare: {}}}"}, "bare or http"},
		{"missing parameters", map[string]string{"toolSets/a.yaml": "spec: {adapter: {type: bare, bare: {}}}", "toolSets/a/t.yaml": "spec: {config: {type: bare, bare: {}}}"}, "parameters is required"},
		{"dangling assignment", map[string]string{"agents/a.yaml": "spec: {}", "agents/a/v.yaml": "spec: {assignments: [{type: toolSetId, toolSetId: 'external_id:missing'}]}"}, "missing local toolSet"},
		{"unscoped tool", map[string]string{"agents/a.yaml": "spec: {}", "agents/a/v.yaml": "spec: {assignments: [{type: toolId, toolId: 'external_id:missing'}]}"}, "tool references must"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range tt.files {
				write(t, root, name, body)
			}
			_, err := Load(root, "test")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

func TestSymlinksAndMissingDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := Load(filepath.Join(root, "missing"), "test"); err == nil {
		t.Fatal("missing root accepted")
	}
	if err := os.Symlink(root, filepath.Join(root, "agents")); err != nil {
		t.Skip(err)
	}
	if _, err := Load(root, "test"); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestSettingsAndBundleKey(t *testing.T) {
	root := t.TempDir()
	if _, err := ReadSettings(filepath.Join(root, "absent"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSettings(filepath.Join(root, "absent"), false); err == nil {
		t.Fatal("explicit missing settings accepted")
	}
	for _, value := range []string{"", "bad,label=x", "has space", strings.Repeat("a", 64), "-leading"} {
		if ValidateBundleKey(value) == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	write(t, root, "cadenya.yaml", "bundleKey: my-bundle\nworkspaceId: development\n")
	s, err := ReadSettings(filepath.Join(root, "cadenya.yaml"), false)
	if err != nil || s.BundleKey != "my-bundle" || s.WorkspaceID != "development" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestMemoryEntryKeyDefaultsAndMask(t *testing.T) {
	root := t.TempDir()
	write(t, root, "memoryLayers/skills.yaml", "spec: {type: MEMORY_LAYER_TYPE_SKILLS}")
	write(t, root, "memoryLayers/skills/postmortem.yaml", "spec: {content: body}")
	write(t, root, "agents/a.yaml", "state: published\nspec: {}")
	write(t, root, "agents/a/v.yaml", "spec: {memoryLayerAssignments: [{memoryLayerId: 'external_id:skills', position: 0}]}")
	b, err := Load(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	entry := b.Resources[Key{Kind: MemoryEntry, Parent: "skills", ExternalID: "postmortem"}]
	if entry.Spec.(*MemoryEntrySpec).Key != "postmortem" || entry.Mask != "content,key" {
		t.Fatalf("key %q mask %q", entry.Spec.(*MemoryEntrySpec).Key, entry.Mask)
	}
	if b.Resources[Key{Kind: Agent, ExternalID: "a"}].State != StatePublished {
		t.Fatal("state not loaded")
	}
}
