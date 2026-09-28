package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode"

	cadenya "go.cadenya.com/cadenya-go"
)

// Load reads and validates every resource under dir, stamping each with
// bundleKey. It never contacts the API.
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
	r.Mask = updateMask(kind, raw.(map[string]any))
	return r, nil
}

// updateMask lists the fields an update sends: the metadata the CLI manages,
// and the spec fields the file sets. Fields the file leaves out keep their
// values in Cadenya.
func updateMask(kind Kind, spec map[string]any) string {
	var fields []string
	if kind == MemoryEntry {
		// The API checks entry masks against the entry spec itself, so these
		// paths carry no "spec." prefix, and metadata is always replaced whole.
		// key is always sent: it defaults to the external ID.
		fields = append(fields, "key")
		for k := range spec {
			if k != "key" {
				fields = append(fields, k)
			}
		}
	} else {
		fields = append(fields, "metadata.name", "metadata.externalId", "metadata.labels")
		for k := range spec {
			fields = append(fields, "spec."+k)
		}
	}
	sort.Strings(fields)
	return strings.Join(fields, ",")
}
