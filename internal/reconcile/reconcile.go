// Package reconcile plans and applies bundle-scoped Cadenya resource changes.
package reconcile

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/cadenya/cadenya-config-loader/internal/config"
	cadenya "go.cadenya.com/cadenya-go"
)

type Operation struct {
	Action string `json:"action"`
	config.Key
	File        string `json:"file,omitempty"`
	ID          string `json:"id,omitempty"`
	Host        string `json:"host,omitempty"` // Widgets only: the embed host, after apply.
	Completed   bool   `json:"completed"`
	desired     *config.Resource
	remote      *remote
	assignments []cadenya.VariationAssignment
	layers      []cadenya.VariationMemoryLayerAssignment
	mask        string
}

type Summary struct {
	Creates      int `json:"creates"`
	Updates      int `json:"updates"`
	Deletes      int `json:"deletes"`
	Detaches     int `json:"detaches"`
	StateChanges int `json:"stateChanges"`
}

type Plan struct {
	BundleKey   string       `json:"bundleKey"`
	WorkspaceID string       `json:"workspaceId"`
	Applied     bool         `json:"applied"`
	Summary     Summary      `json:"summary"`
	Operations  []*Operation `json:"operations"`
	client      *cadenya.Client
	current     inventory
	log         *slog.Logger
}

const (
	remoteDraft     = "STATE_DRAFT"
	remotePublished = "STATE_PUBLISHED"
	remoteArchived  = "STATE_ARCHIVED"
)

// Build reads everything, then orders the writes:
//
//  1. Detach assignments that point at resources this apply deletes.
//  2. Unarchive or unpublish agents. A published agent must be unpublished
//     before its last variation can be deleted.
//  3. Delete, children first. Variations of surviving agents wait for step 6.
//  4. Create or update in dependency order.
//  5. Publish agents, now that their variations exist.
//  6. Delete variations of surviving agents, so a published agent always keeps
//     at least one variation while its replacements are created. Agents that a
//     surviving widget is still bound to wait here too, along with their
//     variations: the widget is re-pointed in step 4, and the API refuses to
//     delete an agent while a widget is bound to it.
//
// A nil log discards log output.
func Build(ctx context.Context, c *cadenya.Client, b *config.Bundle, bundle, workspace string, log *slog.Logger) (*Plan, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	log = log.With("bundle", bundle, "workspace", workspace)
	started := time.Now()
	log.Debug("listing bundle resources")
	current, err := snapshot(ctx, c, bundle)
	if err != nil {
		return nil, explain(err)
	}
	counts := map[config.Kind]int{}
	for key := range current {
		counts[key.Kind]++
	}
	attrs := []any{"total", len(current), "duration_ms", time.Since(started).Milliseconds()}
	for _, kind := range config.Order {
		attrs = append(attrs, string(kind), counts[kind])
	}
	log.Info("found bundle resources", attrs...)
	p := &Plan{BundleKey: bundle, WorkspaceID: workspace, Operations: []*Operation{}, client: c, current: current, log: log}
	// Resolve every read and ownership check before executing any mutation.
	for _, kind := range config.Order {
		for _, r := range b.Sorted(kind) {
			if current[r.Key] != nil {
				continue
			}
			parent := ""
			if r.Parent != "" {
				pr := current[parentKey(r.Key)]
				if pr == nil {
					continue
				} // The parent will be created in this apply.
				parent = pr.ID
			}
			log.Debug("checking external ID is unclaimed", "resource", r.Key.String())
			if err := retrieve(ctx, c, r.Key, parent); err != nil {
				return nil, explain(err)
			}
		}
	}
	// The API ignores spec.type on update, so a changed type would silently
	// stay the same. Say so instead.
	for _, r := range b.Sorted(config.MemoryLayer) {
		old := current[r.Key]
		if old == nil {
			continue
		}
		have, ok := old.Spec.(*cadenya.MemoryLayerSpec)
		want := r.Spec.(*cadenya.MemoryLayerSpecParam).Type
		if ok && have != nil && have.Type != want {
			return nil, fmt.Errorf("%s: a memory layer's spec.type cannot change (%s to %s); give it a new externalId to replace it", r.File, have.Type, want)
		}
	}
	var removed []*remote
	retiredIDs := map[string]bool{}
	retired := map[string]*remote{}
	for key, r := range current {
		if b.Resources[key] == nil {
			removed = append(removed, r)
			retiredIDs[r.ID] = true
			retired[r.ID] = r
		}
	}
	sort.Slice(removed, func(i, j int) bool {
		a, z := rank(removed[i].Key.Kind), rank(removed[j].Key.Kind)
		if a != z {
			return a > z
		}
		return removed[i].Key.String() < removed[j].Key.String()
	})
	for _, r := range removed {
		if err := checkChildren(ctx, c, r, bundle); err != nil {
			return nil, fmt.Errorf("check delete %s: %w", r.Key, explain(err))
		}
	}
	if len(removed) > 0 {
		log.Debug("checking the workspace for outside references to deleted resources", "deletes", len(removed))
		others, err := scanOutside(ctx, c, bundle, retired)
		if err != nil {
			return nil, explain(err)
		}
		// Cadenya won't delete a published agent's last variation. Say so now,
		// before any write, rather than after the rest of the apply.
		for _, r := range b.Sorted(config.Agent) {
			old := current[r.Key]
			if old == nil || old.State != remotePublished || r.State == config.StateDraft {
				continue
			}
			kept := others.variations[old.ID]
			for key := range b.Resources {
				if key.Kind == config.Variation && key.Parent == r.ExternalID {
					kept++
				}
			}
			if kept == 0 {
				return nil, fmt.Errorf("%s: this apply would delete every variation of a published agent; keep one, or set state: draft", r.File)
			}
		}
	}
	// Agents still bound to a surviving widget, by canonical ID.
	bound := map[string]bool{}
	for key, r := range current {
		if key.Kind != config.Widget || b.Resources[key] == nil {
			continue
		}
		spec, ok := r.Spec.(*cadenya.WidgetSpec)
		if !ok || spec == nil {
			return nil, fmt.Errorf("%s has no spec in API response", key)
		}
		bound[spec.AgentID] = true
	}
	// Late deletes wait for step 6. Anything a late variation assigns must be
	// detached before step 3 deletes it.
	late := func(r *remote) bool {
		switch r.Key.Kind {
		case config.Variation:
			return b.Resources[parentKey(r.Key)] != nil || bound[r.ParentID]
		case config.Agent:
			return bound[r.ID]
		}
		return false
	}
	for _, r := range b.Sorted(config.Widget) {
		for _, ref := range config.WidgetTargets(r.Spec.(*cadenya.WidgetSpec)) {
			if retiredIDs[*ref.ID] {
				return nil, fmt.Errorf("%s points at a resource that this bundle would delete: %s", r.File, *ref.ID)
			}
		}
		old := current[r.Key]
		if old == nil {
			continue
		}
		// A pinned variation can't be deleted out from under its widget.
		if spec, ok := old.Spec.(*cadenya.WidgetSpec); ok && spec != nil && spec.VariationID != nil && retiredIDs[*spec.VariationID] && !strings.Contains(","+r.Mask+",", ",spec.variationId,") {
			return nil, fmt.Errorf("%s: its pinned variation is being deleted; set spec.variationId (null to unpin)", r.File)
		}
	}
	for _, r := range b.Sorted(config.Variation) {
		for _, ref := range config.AssignmentTargets(r.Spec.(*cadenya.AgentVariationSpec)) {
			if retiredIDs[*ref.ID] {
				return nil, fmt.Errorf("%s assigns a resource that this bundle would delete: %s", r.File, *ref.ID)
			}
		}
		old := current[r.Key]
		if old == nil {
			continue
		}
		op, err := detach(old, retiredIDs)
		if err != nil {
			return nil, err
		}
		if op == nil {
			continue
		}
		for _, field := range strings.Split(op.mask, ",") {
			if !strings.Contains(","+r.Mask+",", ","+field+",") {
				return nil, fmt.Errorf("%s: include %s (or []) to remove assignments to deleted resources", r.File, field)
			}
		}
		p.add(op)
	}
	for _, r := range removed {
		if r.Key.Kind != config.Variation || !late(r) {
			continue
		}
		op, err := detach(r, retiredIDs)
		if err != nil {
			return nil, err
		}
		if op != nil {
			p.add(op)
		}
	}
	var publishes []*Operation
	for _, r := range b.Sorted(config.Agent) {
		if r.State == "" {
			continue
		}
		old := current[r.Key]
		state, id := remoteDraft, ""
		if old != nil {
			state, id = old.State, old.ID
		}
		if state == remoteArchived {
			p.add(&Operation{Action: "unarchive", Key: r.Key, ID: id})
			state = remoteDraft
		}
		switch {
		case r.State == config.StateDraft && state == remotePublished:
			p.add(&Operation{Action: "unpublish", Key: r.Key, ID: id})
		case r.State == config.StatePublished && state != remotePublished:
			publishes = append(publishes, &Operation{Action: "publish", Key: r.Key, ID: id})
		}
	}
	// A published agent being deleted is unpublished right before its
	// variations go. A late one waits until its widget has moved off it.
	unpublish := func(early bool) {
		for _, r := range removed {
			if r.Key.Kind == config.Agent && r.State == remotePublished && late(r) != early {
				p.add(&Operation{Action: "unpublish", Key: r.Key, ID: r.ID})
			}
		}
	}
	unpublish(true)
	for _, r := range removed {
		if !late(r) {
			p.add(&Operation{Action: "delete", Key: r.Key, ID: r.ID, remote: r})
		}
	}
	for _, kind := range config.Order {
		for _, r := range b.Sorted(kind) {
			action := "create"
			id := ""
			if old := current[r.Key]; old != nil {
				action = "update"
				id = old.ID
			}
			p.add(&Operation{Action: action, Key: r.Key, File: r.File, ID: id, desired: r, remote: current[r.Key]})
		}
	}
	for _, op := range publishes {
		p.add(op)
	}
	unpublish(false)
	for _, r := range removed {
		if late(r) {
			p.add(&Operation{Action: "delete", Key: r.Key, ID: r.ID, remote: r})
		}
	}
	log.Info("plan ready", "creates", p.Summary.Creates, "updates", p.Summary.Updates, "deletes", p.Summary.Deletes, "detaches", p.Summary.Detaches, "state_changes", p.Summary.StateChanges, "duration_ms", time.Since(started).Milliseconds())
	return p, nil
}

func (p *Plan) add(op *Operation) {
	switch op.Action {
	case "create":
		p.Summary.Creates++
	case "update":
		p.Summary.Updates++
	case "delete":
		p.Summary.Deletes++
	case "detach":
		p.Summary.Detaches++
	default:
		p.Summary.StateChanges++
	}
	p.Operations = append(p.Operations, op)
}

// detach returns an update that drops a remote variation's assignments to
// retired resources, or nil when it has none.
func detach(old *remote, retired map[string]bool) (*Operation, error) {
	spec, ok := old.Spec.(*cadenya.AgentVariationSpec)
	if !ok || spec == nil {
		return nil, fmt.Errorf("%s has no spec in API response", old.Key)
	}
	var fields []string
	kept := make([]cadenya.VariationAssignment, 0, len(spec.Assignments))
	for _, a := range spec.Assignments {
		one := &cadenya.AgentVariationSpec{Assignments: []cadenya.VariationAssignment{a}}
		refs := config.AssignmentTargets(one)
		if len(refs) > 0 && retired[*refs[0].ID] {
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) != len(spec.Assignments) {
		fields = append(fields, "spec.assignments")
	}
	layers := make([]cadenya.VariationMemoryLayerAssignment, 0, len(spec.MemoryLayerAssignments))
	for _, a := range spec.MemoryLayerAssignments {
		if !retired[a.MemoryLayerID] {
			layers = append(layers, a)
		}
	}
	if len(layers) != len(spec.MemoryLayerAssignments) {
		fields = append(fields, "spec.memoryLayerAssignments")
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return &Operation{Action: "detach", Key: old.Key, ID: old.ID, remote: old, assignments: kept, layers: layers, mask: strings.Join(fields, ",")}, nil
}

func rank(k config.Kind) int {
	for i, v := range config.Order {
		if k == v {
			return i
		}
	}
	return -1
}

// Apply stops at the first error; Completed records partial progress for CI.
// A plan can be executed once. Build a fresh plan before retrying a failed apply.
func (p *Plan) Apply(ctx context.Context) error {
	if p.Applied {
		return fmt.Errorf("plan has already been applied; build a fresh plan")
	}
	for _, op := range p.Operations {
		if op.Completed {
			return fmt.Errorf("plan was partially applied; build a fresh plan")
		}
	}
	ids := map[config.Key]string{}
	for key, r := range p.current {
		ids[key] = r.ID
	}
	started := time.Now()
	p.log.Info("applying plan", "operations", len(p.Operations))
	for i, op := range p.Operations {
		if err := ctx.Err(); err != nil {
			p.log.Warn("apply stopped", "reason", err.Error(), "completed", i, "remaining", len(p.Operations)-i)
			return err
		}
		opStarted := time.Now()
		p.log.Debug("starting operation", "action", op.Action, "kind", string(op.Kind), "resource", op.Key.String())
		var err error
		switch op.Action {
		case "detach":
			_, err = p.client.Agents().Variations().Update(ctx, op.remote.ParentID, op.ID, &cadenya.AgentVariationUpdateParams{Spec: &cadenya.AgentVariationSpec{Assignments: op.assignments, MemoryLayerAssignments: op.layers}, UpdateMask: &op.mask})
		case "delete":
			err = deleteRemote(ctx, p.client, op.remote)
			if err == nil {
				delete(ids, op.Key)
			}
		case "publish", "unpublish", "unarchive":
			if op.ID == "" {
				op.ID = ids[op.Key] // Created earlier in this apply.
			}
			if op.ID == "" {
				err = fmt.Errorf("agent was not resolved")
				break
			}
			err = transition(ctx, p.client, op.Action, op.ID)
		default:
			r := op.desired
			parent := ""
			if r.Parent != "" {
				parent = ids[parentKey(r.Key)]
				if parent == "" {
					err = fmt.Errorf("parent was not resolved")
					break
				}
			}
			r, err = resolve(r, ids)
			if err != nil {
				break
			}
			var id, host string
			id, host, err = upsert(ctx, p.client, r, op.remote, parent)
			if err == nil {
				ids[r.Key] = id
				op.ID, op.Host = id, host
			}
		}
		attrs := []any{"action", op.Action, "kind", string(op.Kind), "resource", op.Key.String(), "id", op.ID, "duration_ms", time.Since(opStarted).Milliseconds()}
		if err != nil {
			err = explain(err)
			p.log.Error("operation failed", append(attrs, "error", err.Error(), "completed", i, "remaining", len(p.Operations)-i)...)
			return fmt.Errorf("%s %s: %w", op.Action, op.Key, err)
		}
		if op.Host != "" {
			attrs = append(attrs, "host", op.Host)
		}
		p.log.Info("operation succeeded", attrs...)
		op.Completed = true
	}
	p.Applied = true
	p.log.Info("apply complete", "creates", p.Summary.Creates, "updates", p.Summary.Updates, "deletes", p.Summary.Deletes, "detaches", p.Summary.Detaches, "state_changes", p.Summary.StateChanges, "duration_ms", time.Since(started).Milliseconds())
	return nil
}

// resolve returns r with local external_id references in a variation or widget
// replaced by canonical IDs. It clones first so the loaded bundle stays reusable.
func resolve(r *config.Resource, ids map[config.Key]string) (*config.Resource, error) {
	var targets []config.AssignmentTarget
	copyResource := *r
	switch spec := r.Spec.(type) {
	case *cadenya.AgentVariationSpec:
		copySpec := *spec
		copySpec.Assignments = append([]cadenya.VariationAssignment(nil), copySpec.Assignments...)
		copySpec.MemoryLayerAssignments = append([]cadenya.VariationMemoryLayerAssignment(nil), copySpec.MemoryLayerAssignments...)
		for i, a := range copySpec.Assignments {
			if a.ToolID != nil {
				v := *a.ToolID
				copySpec.Assignments[i].ToolID = &v
			}
			if a.ToolSetID != nil {
				v := *a.ToolSetID
				copySpec.Assignments[i].ToolSetID = &v
			}
			if a.SubAgentID != nil {
				v := *a.SubAgentID
				copySpec.Assignments[i].SubAgentID = &v
			}
		}
		copyResource.Spec = &copySpec
		targets = config.AssignmentTargets(&copySpec)
	case *cadenya.WidgetSpec:
		copySpec := *spec
		if spec.VariationID != nil {
			v := *spec.VariationID
			copySpec.VariationID = &v
		}
		copyResource.Spec = &copySpec
		targets = config.WidgetTargets(&copySpec)
	default:
		return r, nil
	}
	for _, ref := range targets {
		key, local, err := config.ReferenceKey(ref.Kind, *ref.ID)
		if err != nil {
			return nil, err
		}
		if local {
			if ids[key] == "" {
				return nil, fmt.Errorf("unresolved %s", key)
			}
			*ref.ID = ids[key]
		}
	}
	return &copyResource, nil
}
