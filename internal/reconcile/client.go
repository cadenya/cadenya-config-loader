package reconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/cadenya/cadenya-config-loader/internal/config"
	cadenya "go.cadenya.com/cadenya-go"
)

type remote struct {
	Key      config.Key
	ID       string
	ParentID string
	Spec     any
	State    string // Agents only, as the API reports it (e.g. STATE_PUBLISHED).
}

type inventory map[config.Key]*remote

func (s inventory) add(kind config.Kind, parent, parentID, bundle string, meta *cadenya.ResourceMetadata, spec any, state string) error {
	if meta == nil || meta.ID == "" || meta.ExternalID == "" {
		return fmt.Errorf("listed %s has no canonical or external ID; refusing to reconcile ambiguous resources", kind)
	}
	if meta.Labels[config.BundleLabel] != bundle {
		return fmt.Errorf("listed %s %q does not have the requested bundle_key; refusing to mutate", kind, meta.ID)
	}
	key := config.Key{Kind: kind, Parent: parent, ExternalID: meta.ExternalID}
	if s[key] != nil {
		return fmt.Errorf("API returned duplicate %s", key)
	}
	s[key] = &remote{Key: key, ID: meta.ID, ParentID: parentID, Spec: spec, State: state}
	return nil
}

func all[T any](ctx context.Context, page *cadenya.Page[T], err error) ([]T, error) {
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, fmt.Errorf("API returned an empty page")
	}
	// The SDK's All method does not detect repeated cursors. Keep automatic
	// pagination here bounded when a server accidentally returns a cursor twice.
	var items []T
	seen := make(map[string]bool)
	for page != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if !page.HasNextPage() {
			return items, nil
		}
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("API repeated a pagination cursor; inventory is incomplete")
		}
		seen[page.NextCursor] = true
		page, err = page.GetNextPage(ctx)
		if err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("API returned an empty continuation page")
}

func snapshot(ctx context.Context, client *cadenya.Client, bundle string) (inventory, error) {
	result := inventory{}
	selector := config.BundleLabel + "=" + bundle
	// The default list filters hide archived resources. Enumerate every state.
	for _, state := range []cadenya.ToolServiceListToolSetsState{"STATE_ACTIVE", "STATE_ARCHIVED"} {
		page, err := client.ToolSets().List(ctx, &cadenya.ToolSetListParams{Labels: &selector, State: &state})
		items, err := all(ctx, page, err)
		if err != nil {
			return nil, fmt.Errorf("list tool sets: %w", err)
		}
		for _, v := range items {
			if err := result.add(config.ToolSet, "", "", bundle, v.Metadata, v.Spec, ""); err != nil {
				return nil, err
			}
			page, err := client.ToolSets().Tools().List(ctx, v.Metadata.ID, &cadenya.ToolListParams{Labels: &selector, States: []cadenya.ToolServiceListToolsStates{"STATE_AVAILABLE", "STATE_OMITTED", "STATE_ARCHIVED"}})
			children, err := all(ctx, page, err)
			if err != nil {
				return nil, fmt.Errorf("list tools in %s: %w", v.Metadata.ExternalID, err)
			}
			for _, child := range children {
				if err := result.add(config.Tool, v.Metadata.ExternalID, v.Metadata.ID, bundle, child.Metadata, child.Spec, ""); err != nil {
					return nil, err
				}
			}
		}
	}
	page, err := client.MemoryLayers().List(ctx, &cadenya.MemoryLayerListParams{Labels: &selector})
	layers, err := all(ctx, page, err)
	if err != nil {
		return nil, fmt.Errorf("list memory layers: %w", err)
	}
	for _, v := range layers {
		if err := result.add(config.MemoryLayer, "", "", bundle, v.Metadata, v.Spec, ""); err != nil {
			return nil, err
		}
		page, err := client.MemoryLayers().Entries().List(ctx, v.Metadata.ID, &cadenya.MemoryEntryListParams{Labels: &selector})
		children, err := all(ctx, page, err)
		if err != nil {
			return nil, fmt.Errorf("list memory entries in %s: %w", v.Metadata.ExternalID, err)
		}
		for _, child := range children {
			if err := result.add(config.MemoryEntry, v.Metadata.ExternalID, v.Metadata.ID, bundle, child.Metadata, child.Spec, ""); err != nil {
				return nil, err
			}
		}
	}
	for _, state := range []cadenya.AgentServiceListAgentsState{"STATE_DRAFT", "STATE_PUBLISHED", "STATE_ARCHIVED"} {
		page, err := client.Agents().List(ctx, &cadenya.AgentListParams{Labels: &selector, State: &state})
		items, err := all(ctx, page, err)
		if err != nil {
			return nil, fmt.Errorf("list agents: %w", err)
		}
		for _, v := range items {
			if err := result.add(config.Agent, "", "", bundle, v.Metadata, v.Spec, string(v.State)); err != nil {
				return nil, err
			}
			page, err := client.Agents().Variations().List(ctx, v.Metadata.ID, &cadenya.AgentVariationListParams{Labels: &selector})
			children, err := all(ctx, page, err)
			if err != nil {
				return nil, fmt.Errorf("list variations in %s: %w", v.Metadata.ExternalID, err)
			}
			for _, child := range children {
				if err := result.add(config.Variation, v.Metadata.ExternalID, v.Metadata.ID, bundle, child.Metadata, child.Spec, ""); err != nil {
					return nil, err
				}
			}
		}
	}
	// Unlike the other lists, the widget list includes archived widgets.
	widgetPage, err := client.Widgets().List(ctx, &cadenya.WidgetListParams{Labels: &selector})
	widgets, err := all(ctx, widgetPage, err)
	if err != nil {
		return nil, fmt.Errorf("list widgets: %w", err)
	}
	for _, v := range widgets {
		if err := result.add(config.Widget, "", "", bundle, v.Metadata, v.Spec, ""); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func retrieve(ctx context.Context, c *cadenya.Client, key config.Key, parent string) error {
	id := "external_id:" + key.ExternalID
	var err error
	switch key.Kind {
	case config.ToolSet:
		_, err = c.ToolSets().Retrieve(ctx, id, nil)
	case config.Tool:
		_, err = c.ToolSets().Tools().Retrieve(ctx, parent, id, nil)
	case config.MemoryLayer:
		_, err = c.MemoryLayers().Retrieve(ctx, id, nil)
	case config.MemoryEntry:
		_, err = c.MemoryLayers().Entries().Retrieve(ctx, parent, id, nil)
	case config.Agent:
		_, err = c.Agents().Retrieve(ctx, id, nil)
	case config.Variation:
		_, err = c.Agents().Variations().Retrieve(ctx, parent, id, nil)
	case config.Widget:
		_, err = c.Widgets().Retrieve(ctx, id, nil)
	}
	var apiErr *cadenya.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check ownership of %s: %w", key, err)
	}
	return fmt.Errorf("%s already exists outside the selected bundle; choose a different externalId or explicitly assign its bundle_key before importing it", key)
}

func parentKey(k config.Key) config.Key {
	kind := config.ToolSet
	switch k.Kind {
	case config.Variation:
		kind = config.Agent
	case config.MemoryEntry:
		kind = config.MemoryLayer
	}
	return config.Key{Kind: kind, ExternalID: k.Parent}
}

// deleteRemote deletes r. A 404 counts as success: the SDK retries DELETE, and
// a retry after a lost response finds the resource already gone.
func deleteRemote(ctx context.Context, c *cadenya.Client, r *remote) error {
	err := deleteByKind(ctx, c, r)
	var apiErr *cadenya.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
		return nil
	}
	return err
}

func deleteByKind(ctx context.Context, c *cadenya.Client, r *remote) error {
	switch r.Key.Kind {
	case config.ToolSet:
		return c.ToolSets().Delete(ctx, r.ID, nil)
	case config.Tool:
		return c.ToolSets().Tools().Delete(ctx, r.ParentID, r.ID, nil)
	case config.MemoryLayer:
		return c.MemoryLayers().Delete(ctx, r.ID, nil)
	case config.MemoryEntry:
		return c.MemoryLayers().Entries().Delete(ctx, r.ParentID, r.ID, nil)
	case config.Agent:
		return c.Agents().Delete(ctx, r.ID, nil)
	case config.Variation:
		return c.Agents().Variations().Delete(ctx, r.ParentID, r.ID, nil)
	case config.Widget:
		return c.Widgets().Delete(ctx, r.ID, nil)
	}
	return fmt.Errorf("unknown resource kind %s", r.Key.Kind)
}

// upsert creates or updates r and returns its canonical ID, plus the embed
// host for a widget.
func upsert(ctx context.Context, c *cadenya.Client, r *config.Resource, existing *remote, parent string) (string, string, error) {
	create := &r.Metadata
	update := &cadenya.UpdateResourceMetadata{Name: create.Name, ExternalID: create.ExternalID, Labels: create.Labels}
	var metadata *cadenya.ResourceMetadata
	var host string
	switch r.Kind {
	case config.ToolSet:
		var v *cadenya.ToolSet
		var err error
		if existing == nil {
			v, err = c.ToolSets().Create(ctx, &cadenya.ToolSetCreateParams{Metadata: create, Spec: r.Spec.(*cadenya.ToolSetSpec)})
		} else {
			v, err = c.ToolSets().Update(ctx, existing.ID, &cadenya.ToolSetUpdateParams{Metadata: update, Spec: r.Spec.(*cadenya.ToolSetSpec), UpdateMask: &r.Mask})
		}
		if err != nil {
			return "", "", err
		}
		if v != nil {
			metadata = v.Metadata
		}
	case config.Tool:
		var v *cadenya.Tool
		var err error
		if existing == nil {
			v, err = c.ToolSets().Tools().Create(ctx, parent, &cadenya.ToolCreateParams{Metadata: create, Spec: r.Spec.(*cadenya.ToolSpec)})
		} else {
			v, err = c.ToolSets().Tools().Update(ctx, parent, existing.ID, &cadenya.ToolUpdateParams{Metadata: update, Spec: r.Spec.(*cadenya.ToolSpec), UpdateMask: &r.Mask})
		}
		if err != nil {
			return "", "", err
		}
		if v != nil {
			metadata = v.Metadata
		}
	case config.MemoryLayer:
		var v *cadenya.MemoryLayer
		var err error
		if existing == nil {
			v, err = c.MemoryLayers().Create(ctx, &cadenya.MemoryLayerCreateParams{Metadata: create, Spec: r.Spec.(*cadenya.MemoryLayerSpecParam)})
		} else {
			v, err = c.MemoryLayers().Update(ctx, existing.ID, &cadenya.MemoryLayerUpdateParams{Metadata: update, Spec: r.Spec.(*cadenya.MemoryLayerSpecParam), UpdateMask: &r.Mask})
		}
		if err != nil {
			return "", "", err
		}
		if v != nil {
			metadata = v.Metadata
		}
	case config.MemoryEntry:
		spec := r.Spec.(*config.MemoryEntrySpec)
		var v *cadenya.MemoryEntryDetail
		var err error
		if existing == nil {
			content := ""
			if spec.Content != nil {
				content = *spec.Content
			}
			v, err = c.MemoryLayers().Entries().Create(ctx, parent, &cadenya.MemoryEntryCreateParams{Metadata: create, Spec: &cadenya.MemoryEntryCreateSpec{Content: &cadenya.MemoryEntryCreateSpec_Content{Type: "content", Content: content, Key: spec.Key, Description: spec.Description}}})
		} else {
			v, err = c.MemoryLayers().Entries().Update(ctx, parent, existing.ID, &cadenya.MemoryEntryUpdateParams{Metadata: update, Spec: &cadenya.MemoryEntryUpdateSpec{Key: &spec.Key, Description: spec.Description, Content: spec.Content}, UpdateMask: &r.Mask})
		}
		if err != nil {
			return "", "", err
		}
		if v != nil {
			metadata = v.Metadata
		}
	case config.Agent:
		var v *cadenya.Agent
		var err error
		if existing == nil {
			v, err = c.Agents().Create(ctx, &cadenya.AgentCreateParams{Metadata: create, Spec: r.Spec.(*cadenya.AgentSpec)})
		} else {
			v, err = c.Agents().Update(ctx, existing.ID, &cadenya.AgentUpdateParams{Metadata: update, Spec: r.Spec.(*cadenya.AgentSpec), UpdateMask: &r.Mask})
		}
		if err != nil {
			return "", "", err
		}
		if v != nil {
			metadata = v.Metadata
		}
	case config.Variation:
		var v *cadenya.AgentVariation
		var err error
		if existing == nil {
			v, err = c.Agents().Variations().Create(ctx, parent, &cadenya.AgentVariationCreateParams{Metadata: create, Spec: r.Spec.(*cadenya.AgentVariationSpec)})
		} else {
			v, err = c.Agents().Variations().Update(ctx, parent, existing.ID, &cadenya.AgentVariationUpdateParams{Metadata: update, Spec: r.Spec.(*cadenya.AgentVariationSpec), UpdateMask: &r.Mask})
		}
		if err != nil {
			return "", "", err
		}
		if v != nil {
			metadata = v.Metadata
		}
	case config.Widget:
		var v *cadenya.Widget
		var err error
		if existing == nil {
			v, err = c.Widgets().Create(ctx, &cadenya.WidgetCreateParams{Metadata: create, Spec: r.Spec.(*cadenya.WidgetSpec)})
		} else {
			v, err = c.Widgets().Update(ctx, existing.ID, &cadenya.WidgetUpdateParams{Metadata: update, Spec: r.Spec.(*cadenya.WidgetSpec), UpdateMask: &r.Mask})
		}
		if err != nil {
			return "", "", err
		}
		if v != nil {
			metadata = v.Metadata
			if v.Info != nil {
				host = v.Info.Host
			}
		}
	}
	if metadata == nil || metadata.ID == "" {
		return "", "", fmt.Errorf("API response is missing metadata.id")
	}
	return metadata.ID, host, nil
}

// transition moves an agent between lifecycle states.
func transition(ctx context.Context, c *cadenya.Client, action, id string) error {
	var err error
	switch action {
	case "publish":
		_, err = c.Agents().Publish(ctx, id, nil)
	case "unpublish":
		_, err = c.Agents().Unpublish(ctx, id, nil)
	case "unarchive":
		_, err = c.Agents().Unarchive(ctx, id, nil)
	default:
		err = fmt.Errorf("unknown agent transition %s", action)
	}
	return err
}

// Parent deletes can cascade. Refuse to delete another bundle's/manual children.
func checkChildren(ctx context.Context, c *cadenya.Client, r *remote, bundle string) error {
	check := func(m *cadenya.ResourceMetadata) error {
		if m == nil || m.Labels[config.BundleLabel] != bundle {
			return fmt.Errorf("cannot delete %s: it contains children outside bundle %q", r.Key, bundle)
		}
		return nil
	}
	switch r.Key.Kind {
	case config.Agent:
		page, err := c.Agents().Variations().List(ctx, r.ID, nil)
		items, err := all(ctx, page, err)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := check(item.Metadata); err != nil {
				return err
			}
		}
		// The API refuses to delete an agent while a widget is bound to it.
		agentID := r.ID
		widgetPage, err := c.Widgets().List(ctx, &cadenya.WidgetListParams{AgentID: &agentID})
		widgets, err := all(ctx, widgetPage, err)
		if err != nil {
			return err
		}
		for _, w := range widgets {
			if w.Metadata == nil || w.Metadata.Labels[config.BundleLabel] != bundle {
				return fmt.Errorf("cannot delete %s: a widget outside bundle %q is bound to it", r.Key, bundle)
			}
		}
	case config.MemoryLayer:
		page, err := c.MemoryLayers().Entries().List(ctx, r.ID, nil)
		items, err := all(ctx, page, err)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := check(item.Metadata); err != nil {
				return err
			}
		}
	case config.ToolSet:
		spec, ok := r.Spec.(*cadenya.ToolSetSpec)
		if !ok || spec == nil || spec.Adapter == nil {
			return fmt.Errorf("%s has no adapter in API response", r.Key)
		}
		// Synced children are owned by the adapter and removed with their set.
		if spec.Adapter.MCP != nil || spec.Adapter.OpenAPI != nil {
			return nil
		}
		page, err := c.ToolSets().Tools().List(ctx, r.ID, &cadenya.ToolListParams{States: []cadenya.ToolServiceListToolsStates{"STATE_AVAILABLE", "STATE_OMITTED", "STATE_ARCHIVED"}})
		items, err := all(ctx, page, err)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := check(item.Metadata); err != nil {
				return err
			}
		}
	}
	return nil
}

// outside describes what lies outside the bundle but depends on it.
type outside struct {
	// variations counts variations outside the bundle, by canonical agent ID.
	variations map[string]int
}

// scanOutside lists every agent, variation, and widget in the workspace and
// fails if one outside the bundle refers to a resource the plan deletes. The
// API refuses some of those deletes (an assigned memory layer, a pinned
// variation) and allows others (a tool), but either way deleting it would
// break a resource this bundle doesn't own. It lists the whole workspace, so
// Build calls it only when the plan deletes something.
func scanOutside(ctx context.Context, c *cadenya.Client, bundle string, retired map[string]*remote) (*outside, error) {
	result := &outside{variations: map[string]int{}}
	name := func(id string) string { return retired[id].Key.String() }
	for _, state := range []cadenya.AgentServiceListAgentsState{"STATE_DRAFT", "STATE_PUBLISHED", "STATE_ARCHIVED"} {
		page, err := c.Agents().List(ctx, &cadenya.AgentListParams{State: &state})
		agents, err := all(ctx, page, err)
		if err != nil {
			return nil, fmt.Errorf("list agents: %w", err)
		}
		for _, a := range agents {
			if a.Metadata == nil {
				return nil, fmt.Errorf("listed agent has no metadata")
			}
			page, err := c.Agents().Variations().List(ctx, a.Metadata.ID, nil)
			variations, err := all(ctx, page, err)
			if err != nil {
				return nil, fmt.Errorf("list variations in %s: %w", a.Metadata.ID, err)
			}
			for _, v := range variations {
				if v.Metadata == nil || v.Metadata.Labels[config.BundleLabel] == bundle {
					continue
				}
				result.variations[a.Metadata.ID]++
				if v.Spec == nil {
					continue
				}
				for _, ref := range config.AssignmentTargets(v.Spec) {
					if retired[*ref.ID] != nil {
						return nil, fmt.Errorf("cannot delete %s: variation %s of agent %s, outside bundle %q, assigns it", name(*ref.ID), v.Metadata.ID, a.Metadata.ID, bundle)
					}
				}
			}
		}
	}
	page, err := c.Widgets().List(ctx, nil)
	widgets, err := all(ctx, page, err)
	if err != nil {
		return nil, fmt.Errorf("list widgets: %w", err)
	}
	for _, w := range widgets {
		if w.Metadata == nil || w.Metadata.Labels[config.BundleLabel] == bundle || w.Spec == nil {
			continue
		}
		if w.Spec.VariationID != nil && retired[*w.Spec.VariationID] != nil {
			return nil, fmt.Errorf("cannot delete %s: widget %s, outside bundle %q, pins it", name(*w.Spec.VariationID), w.Metadata.ID, bundle)
		}
	}
	return result, nil
}
