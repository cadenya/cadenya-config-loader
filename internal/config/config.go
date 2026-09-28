// Package config loads a source-controlled Cadenya bundle without contacting the API.
package config

import (
	"fmt"
	"os"
	"sort"

	cadenya "go.cadenya.com/cadenya-go"
)

// BundleLabel is the label that marks which bundle owns a resource.
const BundleLabel = "bundle_key"

// Settings are the values cadenya.yaml can set. Flags and environment
// variables override them.
type Settings struct {
	BundleKey   string `json:"bundleKey"`
	WorkspaceID string `json:"workspaceId"`
	BaseURL     string `json:"baseUrl"`
	ResourceDir string `json:"resourceDir"`
}

// Kind is a resource type, as it appears in plans and reports.
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

// Key identifies a resource within a bundle: its kind, its external ID, and
// for a child, its parent's external ID.
type Key struct {
	Kind       Kind   `json:"kind"`
	Parent     string `json:"parent,omitempty"`
	ExternalID string `json:"externalId"`
}

// String renders the key as it appears in plans, like "tool frontend/fetch".
func (k Key) String() string {
	if k.Parent != "" {
		return string(k.Kind) + " " + k.Parent + "/" + k.ExternalID
	}
	return string(k.Kind) + " " + k.ExternalID
}

// Resource is one YAML file: the resource it describes and the update mask
// its fields imply.
type Resource struct {
	Key
	File     string
	Metadata cadenya.CreateResourceMetadata
	Spec     any
	Mask     string
	State    string // Agents only: StateDraft, StatePublished, or empty.
}

// Bundle is every resource in a resource directory, by key.
type Bundle struct {
	Resources map[Key]*Resource
}

// Sorted returns the bundle's resources of one kind in a stable order.
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
