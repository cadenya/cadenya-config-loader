// Package config loads a source-controlled Cadenya bundle without contacting the API.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"

	cadenya "go.cadenya.com/cadenya-go"
	"go.yaml.in/yaml/v3"
)

const BundleLabel = "bundle_key"

type Settings struct {
	BundleKey   string `json:"bundleKey"`
	WorkspaceID string `json:"workspaceId"`
	BaseURL     string `json:"baseUrl"`
	ResourceDir string `json:"resourceDir"`
}

type Kind string

const (
	ToolSet     Kind = "toolSet"
	Tool        Kind = "tool"
	MemoryLayer Kind = "memoryLayer"
	MemoryEntry Kind = "memoryEntry"
	Agent       Kind = "agent"
	Variation   Kind = "agentVariation"
	Widget      Kind = "widget"
)

// Order is dependency order: variations assign tools, memory layers, and
// agents, and widgets bind an agent and optionally pin one of its variations.
var Order = []Kind{ToolSet, Tool, MemoryLayer, MemoryEntry, Agent, Variation, Widget}

// Agent lifecycle states a bundle can request. Omitted leaves the state alone.
const (
	StateDraft     = "draft"
	StatePublished = "published"
)

// MemoryEntrySpec is the YAML shape of a memory entry. The API splits it
// across create and update messages, and stores content separately.
type MemoryEntrySpec struct {
	Key         string  `json:"key"`
	Description *string `json:"description,omitempty"`
	Content     *string `json:"content,omitempty"`
}

type Key struct {
	Kind       Kind   `json:"kind"`
	Parent     string `json:"parent,omitempty"`
	ExternalID string `json:"externalId"`
}

func (k Key) String() string {
	if k.Parent != "" {
		return string(k.Kind) + " " + k.Parent + "/" + k.ExternalID
	}
	return string(k.Kind) + " " + k.ExternalID
}

type Resource struct {
	Key
	File     string
	Metadata cadenya.CreateResourceMetadata
	Spec     any
	Mask     string
	State    string // Agents only: StateDraft, StatePublished, or empty.
}

type Bundle struct {
	Resources map[Key]*Resource
}

func (b *Bundle) Sorted(kind Kind) []*Resource {
	var result []*Resource
	for key, r := range b.Resources {
		if key.Kind == kind {
			result = append(result, r)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Parent != result[j].Parent {
			return result[i].Parent < result[j].Parent
		}
		return result[i].ExternalID < result[j].ExternalID
	})
	return result
}

// ReadSettings permits an absent default file, but an explicit path must exist.
func ReadSettings(path string, optional bool) (Settings, error) {
	var result Settings
	data, err := yamlJSON(path)
	if optional && os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	err = decode(data, &result)
	if err != nil {
		return result, fmt.Errorf("%s: %w", path, err)
	}
	return result, nil
}

var labelValue = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)

func ValidateBundleKey(value string) error {
	if !labelValue.MatchString(value) {
		return fmt.Errorf("bundle key must be 1–63 characters, start and end with a letter or digit, and contain only letters, digits, '.', '_' or '-'")
	}
	return nil
}

func Load(dir, bundleKey string) (*Bundle, error) {
	if err := ValidateBundleKey(bundleKey); err != nil {
		return nil, err
	}
	entries, err := readDir(dir)
	if err != nil {
		return nil, fmt.Errorf("resource directory: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "toolSets" && e.Name() != "memoryLayers" && e.Name() != "agents" && e.Name() != "widgets" && !strings.HasPrefix(e.Name(), ".") {
			return nil, fmt.Errorf("%s: unexpected directory %q (expected toolSets, memoryLayers, agents, or widgets)", dir, e.Name())
		}
	}
	b := &Bundle{Resources: make(map[Key]*Resource)}
	for _, group := range []struct {
		name          string
		parent, child Kind
	}{{"toolSets", ToolSet, Tool}, {"memoryLayers", MemoryLayer, MemoryEntry}, {"agents", Agent, Variation}, {"widgets", Widget, ""}} {
		path := filepath.Join(dir, group.name)
		entries, err := readDir(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		parents := map[string]*Resource{}
		for _, e := range entries {
			if e.IsDir() || !isYAML(e.Name()) {
				continue
			}
			r, err := loadResource(filepath.Join(path, e.Name()), group.parent, "", bundleKey)
			if err != nil {
				return nil, err
			}
			stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			if parents[stem] != nil {
				return nil, fmt.Errorf("%s: duplicate parent filename %q", path, stem)
			}
			parents[stem] = r
			if err := b.add(r); err != nil {
				return nil, err
			}
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if group.child == "" {
				return nil, fmt.Errorf("%s: unexpected directory %q (%s have no children)", path, e.Name(), group.name)
			}
			parent := parents[e.Name()]
			if parent == nil {
				return nil, fmt.Errorf("%s: directory %q has no matching parent YAML file", path, e.Name())
			}
			children, err := readDir(filepath.Join(path, e.Name()))
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				if child.IsDir() {
					return nil, fmt.Errorf("%s: unexpected nested directory %q", filepath.Join(path, e.Name()), child.Name())
				}
				if !isYAML(child.Name()) {
					continue
				}
				r, err := loadResource(filepath.Join(path, e.Name(), child.Name()), group.child, parent.ExternalID, bundleKey)
				if err != nil {
					return nil, err
				}
				if group.child == Tool {
					adapter := parent.Spec.(*cadenya.ToolSetSpec).Adapter
					cfg := r.Spec.(*cadenya.ToolSpec).Config
					if adapter.Bare == nil && adapter.HTTP == nil {
						return nil, fmt.Errorf("%s: individual tools require a bare or http tool set", r.File)
					}
					if (adapter.Bare != nil) != (cfg.Bare != nil) || (adapter.HTTP != nil) != (cfg.HTTP != nil) {
						return nil, fmt.Errorf("%s: tool config must match its parent adapter", r.File)
					}
				}
				if err := b.add(r); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := b.ValidateReferences(); err != nil {
		return nil, err
	}
	for _, r := range b.Sorted(Agent) {
		if r.State != StatePublished {
			continue
		}
		hasVariation := false
		for key := range b.Resources {
			if key.Kind == Variation && key.Parent == r.ExternalID {
				hasVariation = true
				break
			}
		}
		// The API refuses to publish an agent without a variation. Catch it here
		// rather than after the rest of the bundle has been written.
		if !hasVariation {
			return nil, fmt.Errorf("%s: state published requires at least one variation in this bundle", r.File)
		}
	}
	return b, nil
}

func (b *Bundle) add(r *Resource) error {
	if old := b.Resources[r.Key]; old != nil {
		return fmt.Errorf("%s: duplicate %s (also in %s)", r.File, r.Key, old.File)
	}
	b.Resources[r.Key] = r
	return nil
}

func readDir(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: expected a directory (symlinks are not supported)", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s: symlinks are not supported", filepath.Join(path, e.Name()))
		}
	}
	return entries, nil
}

func isYAML(name string) bool { return filepath.Ext(name) == ".yaml" || filepath.Ext(name) == ".yml" }

func loadResource(path string, kind Kind, parent, bundleKey string) (*Resource, error) {
	data, err := yamlJSON(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		ExternalID *string                        `json:"externalId"`
		State      *string                        `json:"state"`
		Metadata   cadenya.CreateResourceMetadata `json:"metadata"`
		Spec       json.RawMessage                `json:"spec"`
	}
	if err := decode(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if doc.Metadata.ExternalID != nil {
		id = *doc.Metadata.ExternalID
	}
	if doc.ExternalID != nil {
		if doc.Metadata.ExternalID != nil && *doc.Metadata.ExternalID != *doc.ExternalID {
			return nil, fmt.Errorf("%s: externalId conflicts with metadata.externalId", path)
		}
		id = *doc.ExternalID
	}
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "/\\") || strings.IndexFunc(id, unicode.IsControl) >= 0 || strings.TrimSpace(id) != id {
		return nil, fmt.Errorf("%s: externalId must be nonblank and cannot contain slashes or control whitespace", path)
	}
	doc.Metadata.ExternalID = &id
	if doc.Metadata.Name == "" {
		doc.Metadata.Name = id
	}
	if strings.TrimSpace(doc.Metadata.Name) == "" {
		return nil, fmt.Errorf("%s: metadata.name must not be blank", path)
	}
	if doc.Metadata.Labels == nil {
		doc.Metadata.Labels = map[string]string{}
	}
	if v, ok := doc.Metadata.Labels[BundleLabel]; ok && v != bundleKey {
		return nil, fmt.Errorf("%s: label %s conflicts with bundle key", path, BundleLabel)
	}
	doc.Metadata.Labels[BundleLabel] = bundleKey
	if err := validateLabels(doc.Metadata.Labels); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Spec) == 0 || bytes.Equal(doc.Spec, []byte("null")) {
		return nil, fmt.Errorf("%s: spec must be an object", path)
	}
	r := &Resource{Key: Key{kind, parent, id}, File: path, Metadata: doc.Metadata}
	if doc.State != nil {
		if kind != Agent {
			return nil, fmt.Errorf("%s: state is only supported on agents", path)
		}
		if *doc.State != StateDraft && *doc.State != StatePublished {
			return nil, fmt.Errorf("%s: state must be %s or %s", path, StateDraft, StatePublished)
		}
		r.State = *doc.State
	}
	switch kind {
	case ToolSet:
		r.Spec = &cadenya.ToolSetSpec{}
	case Tool:
		r.Spec = &cadenya.ToolSpec{}
	case MemoryLayer:
		r.Spec = &cadenya.MemoryLayerSpecParam{}
	case MemoryEntry:
		r.Spec = &MemoryEntrySpec{}
	case Agent:
		r.Spec = &cadenya.AgentSpec{}
	case Variation:
		r.Spec = &cadenya.AgentVariationSpec{}
	case Widget:
		r.Spec = &cadenya.WidgetSpec{}
	}
	if err := decode(doc.Spec, r.Spec); err != nil {
		return nil, fmt.Errorf("%s: spec: %w", path, err)
	}
	// The generated SDK's union decoders do not reject unknown fields. Walk the
	// selected union variant as well, so typos inside adapters cannot be lost.
	var raw any
	if err := json.Unmarshal(doc.Spec, &raw); err != nil {
		return nil, err
	}
	if err := checkFields(raw, reflect.ValueOf(r.Spec), "spec"); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if entry, ok := r.Spec.(*MemoryEntrySpec); ok && entry.Key == "" {
		entry.Key = id
	}
	if err := validateSpec(r.Spec); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	fields := []string{"metadata.name", "metadata.externalId", "metadata.labels"}
	for k := range raw.(map[string]any) {
		fields = append(fields, "spec."+k)
	}
	if kind == MemoryEntry {
		// The API validates entry masks against the entry spec itself, so its
		// paths carry no "spec." prefix. Metadata is always replaced whole.
		fields = []string{"key"}
		for k := range raw.(map[string]any) {
			if k != "key" {
				fields = append(fields, k)
			}
		}
	}
	sort.Strings(fields)
	r.Mask = strings.Join(fields, ",")
	return r, nil
}

// widgetOrigin is the API's own origin pattern.
var widgetOrigin = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)

var memoryKeyChars = regexp.MustCompile(`^[A-Za-z0-9!_.*'()/-]+$`)

// validateMemoryKey mirrors the API's key rules so bad keys fail validate.
func validateMemoryKey(key string) error {
	switch {
	case !memoryKeyChars.MatchString(key):
		return fmt.Errorf("may contain only ASCII letters, digits, and ! - _ . * ' ( ) /")
	case strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") || strings.Contains(key, "//"):
		return fmt.Errorf("cannot start or end with / or contain //")
	case strings.HasPrefix(key, "cadenya/") || strings.HasPrefix(key, "system/"):
		return fmt.Errorf("cannot start with the reserved prefixes cadenya/ or system/")
	}
	return nil
}

var modelReferenceKey = regexp.MustCompile(`^[^.:]+\.[^.]+$`)

func validateSpec(spec any) error {
	switch s := spec.(type) {
	case *cadenya.ToolSetSpec:
		if s.Adapter == nil {
			return fmt.Errorf("spec.adapter is required")
		}
		if (s.Adapter.Bare != nil && s.Adapter.Bare.Bare == nil) || (s.Adapter.HTTP != nil && s.Adapter.HTTP.HTTP == nil) || (s.Adapter.MCP != nil && s.Adapter.MCP.MCP == nil) || (s.Adapter.OpenAPI != nil && s.Adapter.OpenAPI.OpenAPI == nil) {
			return fmt.Errorf("spec.adapter requires its adapter configuration object")
		}
	case *cadenya.ToolSpec:
		if s.Parameters == nil {
			return fmt.Errorf("spec.parameters is required (use {} for no parameters)")
		}
		if s.Config == nil || (s.Config.Bare == nil && s.Config.HTTP == nil) {
			return fmt.Errorf("spec.config must select bare or http")
		}
		if (s.Config.Bare != nil && s.Config.Bare.Bare == nil) || (s.Config.HTTP != nil && s.Config.HTTP.HTTP == nil) {
			return fmt.Errorf("spec.config requires its adapter configuration object")
		}
		if s.Config.HTTP != nil {
			switch s.Config.HTTP.HTTP.RequestMethod {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				return fmt.Errorf("spec.config.http.requestMethod must be GET, POST, PUT, PATCH, or DELETE")
			}
		}
	case *cadenya.MemoryLayerSpecParam:
		switch s.Type {
		case cadenya.MemoryLayerSpecTypeMemoryLayerTypeSkills:
		case cadenya.MemoryLayerSpecTypeMemoryLayerTypeEpisodic:
			return fmt.Errorf("spec.type: episodic layers are created by the runtime, not by bundles")
		default:
			return fmt.Errorf("spec.type must be %s", cadenya.MemoryLayerSpecTypeMemoryLayerTypeSkills)
		}
	case *cadenya.WidgetSpec:
		if strings.TrimSpace(s.AgentID) == "" {
			return fmt.Errorf("spec.agentId is required")
		}
		if s.VariationID != nil && strings.TrimSpace(*s.VariationID) == "" {
			return fmt.Errorf("spec.variationId must not be blank (use null to unpin)")
		}
		for _, origin := range s.OriginAllowlist {
			if !widgetOrigin.MatchString(origin) {
				return fmt.Errorf("spec.originAllowlist: %q must be an exact origin like https://app.example.com or http://localhost:3000, with no path or wildcard", origin)
			}
		}
	case *MemoryEntrySpec:
		if err := validateMemoryKey(s.Key); err != nil {
			return fmt.Errorf("spec.key %q %w", s.Key, err)
		}
	case *cadenya.AgentVariationSpec:
		if s.ModelConfig != nil {
			id := s.ModelConfig.ModelID
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("spec.modelConfig.modelId is required")
			}
			// Mirrors the API's check, so a malformed ID fails validate instead of
			// failing midway through an apply.
			if !strings.HasPrefix(id, "model_") && !modelReferenceKey.MatchString(id) {
				return fmt.Errorf("spec.modelConfig.modelId %q must be <ai-provider-key>.<model> (external IDs, one dot) or a model_ ID", id)
			}
		}
		for _, a := range s.Assignments {
			if a.AgentPoolID != nil && strings.TrimSpace(a.AgentPoolID.AgentPoolID) == "" {
				return fmt.Errorf("spec.assignments: agentPoolId must not be blank")
			}
		}
	}
	return nil
}

func yamlJSON(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := yaml.NewDecoder(f)
	var node yaml.Node
	if err := d.Decode(&node); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: expected one YAML mapping", path)
	}
	if err := checkYAML(&node); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var value any
	if err := node.Decode(&value); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%s: expected exactly one YAML document", path)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, nil
}

func checkYAML(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("line %d: YAML aliases are not supported", n.Line)
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Tag != "!!str" {
				return fmt.Errorf("line %d: mapping keys must be strings", k.Line)
			}
			if seen[k.Value] {
				return fmt.Errorf("line %d: duplicate key %q", k.Line, k.Value)
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkYAML(child); err != nil {
			return err
		}
	}
	return nil
}

func decode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	return checkFields(raw, reflect.ValueOf(target), "")
}

func checkFields(raw any, v reflect.Value, path string) error {
	if raw == nil {
		switch v.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
			return nil
		default:
			return fmt.Errorf("%s must not be null", path)
		}
	}
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch x := raw.(type) {
	case map[string]any:
		if v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String {
			for key, child := range x {
				if err := checkFields(child, v.MapIndex(reflect.ValueOf(key)), path+"."+key); err != nil {
					return err
				}
			}
			return nil
		}
		if v.Kind() != reflect.Struct {
			return nil
		} // JSON Schema and headers are open maps.
		fields := map[string]reflect.Value{}
		union := false
		for i := 0; i < v.NumField(); i++ {
			tag := strings.Split(v.Type().Field(i).Tag.Get("json"), ",")[0]
			if tag == "-" {
				union = true
			}
			if tag == "-" && v.Field(i).Kind() == reflect.Pointer && !v.Field(i).IsNil() {
				return checkFields(raw, v.Field(i), path)
			}
			if tag != "" && tag != "-" {
				fields[tag] = v.Field(i)
			}
		}
		if union {
			return fmt.Errorf("%s.type must select a union variant", path)
		}
		for k, child := range x {
			fv, ok := fields[k]
			if !ok {
				return fmt.Errorf("unknown field %s.%s", path, k)
			}
			if err := checkFields(child, fv, path+"."+k); err != nil {
				return err
			}
		}
	case []any:
		if v.Kind() == reflect.Slice {
			for i, child := range x {
				if err := checkFields(child, v.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

var dnsPrefix = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

func validateLabels(labels map[string]string) error {
	for key, value := range labels {
		parts := strings.Split(key, "/")
		if len(parts) > 2 || !labelValue.MatchString(parts[len(parts)-1]) {
			return fmt.Errorf("invalid label key %q", key)
		}
		if len(parts) == 2 && (len(parts[0]) > 253 || !dnsPrefix.MatchString(parts[0])) {
			return fmt.Errorf("invalid label key prefix %q", parts[0])
		}
		if value != "" && !labelValue.MatchString(value) {
			return fmt.Errorf("label %q has an invalid value", key)
		}
	}
	return nil
}
