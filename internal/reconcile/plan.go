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

// Operation actions, as they appear in reports.
const (
	ActionCreate    = "create"
	ActionUpdate    = "update"
	ActionDelete    = "delete"
	ActionDetach    = "detach"
	ActionPublish   = "publish"
	ActionUnpublish = "unpublish"
	ActionUnarchive = "unarchive"
)

// Operation is one write in a plan.
type Operation struct {
	Action string `json:"action"`
	config.Key
	File      string `json:"file,omitempty"`
	ID        string `json:"id,omitempty"`
	Host      string `json:"host,omitempty"` // Widgets only: the embed host, after apply.
	Completed bool   `json:"completed"`

	desired *config.Resource // Create and update.
	remote  *remote          // Update, delete, and detach.

	// Detach: the assignments to keep, and the fields they replace.
	assignments []cadenya.VariationAssignment
	layers      []cadenya.VariationMemoryLayerAssignment
	mask        string
}

// Summary counts a plan's operations by kind.
type Summary struct {
	Creates      int `json:"creates"`
	Updates      int `json:"updates"`
	Deletes      int `json:"deletes"`
	Detaches     int `json:"detaches"`
	StateChanges int `json:"stateChanges"`
}

// Plan is an ordered list of writes that brings a workspace in line with a
// bundle. Build it with Build, then run it once with Apply.
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

// Agent states, as the API reports them.
const (
	remoteDraft     = "STATE_DRAFT"
	remotePublished = "STATE_PUBLISHED"
	remoteArchived  = "STATE_ARCHIVED"
)

// Build reads the bundle's resources and everything a delete could affect,
// checks that every write will succeed, and orders the writes:
//
//  1. Detach assignments that point at resources this apply deletes.
//  2. Unarchive or unpublish agents. A published agent being deleted is
//     unpublished here, so its last variation can go.
//  3. Delete, children first.
//  4. Create or update, in dependency order.
//  5. Publish agents, now that their variations exist.
//  6. Delete what had to wait for step 4: variations of agents that stay (a
//     published agent always keeps one while its replacements are created),
//     and agents a widget is moving off of (the API refuses to delete an agent
//     a widget is bound to). A published agent among them is unpublished first.
//
// Build never writes. A nil log discards log output.
func Build(ctx context.Context, c *cadenya.Client, b *config.Bundle, bundle, workspace string, log *slog.Logger) (*Plan, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	log = log.With("bundle", bundle, "workspace", workspace)
	started := time.Now()
	pl := &planner{client: c, bundle: b, key: bundle, log: log}
	if err := pl.discover(ctx); err != nil {
		return nil, err
	}
	for _, check := range []func(context.Context) error{pl.checkCreates, pl.checkMemoryLayerTypes, pl.checkDeletes, pl.checkReferences} {
		if err := check(ctx); err != nil {
			return nil, err
		}
	}
	detaches, err := pl.detaches()
	if err != nil {
		return nil, err
	}
	before, publishes := pl.transitions()

	p := &Plan{BundleKey: bundle, WorkspaceID: workspace, Operations: []*Operation{}, client: c, current: pl.current, log: log}
	p.add(detaches...)
	p.add(before...)
	p.add(pl.unpublishRemoved(false)...)
	p.add(pl.deletes(false)...)
	p.add(pl.upserts()...)
	p.add(publishes...)
	p.add(pl.unpublishRemoved(true)...)
	p.add(pl.deletes(true)...)
	log.Info("plan ready", "creates", p.Summary.Creates, "updates", p.Summary.Updates, "deletes", p.Summary.Deletes, "detaches", p.Summary.Detaches, "state_changes", p.Summary.StateChanges, "duration_ms", time.Since(started).Milliseconds())
	return p, nil
}

func (p *Plan) add(ops ...*Operation) {
	for _, op := range ops {
		switch op.Action {
		case ActionCreate:
			p.Summary.Creates++
		case ActionUpdate:
			p.Summary.Updates++
		case ActionDelete:
			p.Summary.Deletes++
		case ActionDetach:
			p.Summary.Detaches++
		default:
			p.Summary.StateChanges++
		}
		p.Operations = append(p.Operations, op)
	}
}

// planner holds what Build learns while it plans.
type planner struct {
	client  *cadenya.Client
	bundle  *config.Bundle
	key     string // The bundle key.
	log     *slog.Logger
	current inventory
	removed []*remote          // In the workspace but not the bundle, children first.
	retired map[string]*remote // The same, by canonical ID.
	bound   map[string]bool    // Agents a surviving widget is bound to, by canonical ID.
}

// discover lists the bundle's resources and works out what the plan deletes.
func (pl *planner) discover(ctx context.Context) error {
	started := time.Now()
	pl.log.Debug("listing bundle resources")
	current, err := snapshot(ctx, pl.client, pl.key)
	if err != nil {
		return explain(err)
	}
	pl.current = current
	counts := map[config.Kind]int{}
	for key := range current {
		counts[key.Kind]++
	}
	attrs := []any{"total", len(current), "duration_ms", time.Since(started).Milliseconds()}
	for _, kind := range config.Order {
		attrs = append(attrs, string(kind), counts[kind])
	}
	pl.log.Info("found bundle resources", attrs...)

	pl.retired = map[string]*remote{}
	for key, r := range current {
		if pl.bundle.Resources[key] == nil {
			pl.removed = append(pl.removed, r)
			pl.retired[r.ID] = r
		}
	}
	sort.Slice(pl.removed, func(i, j int) bool {
		a, z := rank(pl.removed[i].Key.Kind), rank(pl.removed[j].Key.Kind)
		if a != z {
			return a > z
		}
		return pl.removed[i].Key.String() < pl.removed[j].Key.String()
	})

	pl.bound = map[string]bool{}
	for key, r := range current {
		if key.Kind != config.Widget || pl.bundle.Resources[key] == nil {
			continue
		}
		spec, ok := r.Spec.(*cadenya.WidgetSpec)
		if !ok || spec == nil {
			return fmt.Errorf("%s has no spec in API response", key)
		}
		pl.bound[spec.AgentID] = true
	}
	return nil
}

// late reports whether r's delete waits for step 6.
func (pl *planner) late(r *remote) bool {
	switch r.Key.Kind {
	case config.Variation:
		return pl.bundle.Resources[parentKey(r.Key)] != nil || pl.bound[r.ParentID]
	case config.Agent:
		return pl.bound[r.ID]
	}
	return false
}

// checkCreates makes sure nothing the plan creates already exists outside the
// bundle. The CLI never adopts a resource it doesn't own.
func (pl *planner) checkCreates(ctx context.Context) error {
	for _, kind := range config.Order {
		for _, r := range pl.bundle.Sorted(kind) {
			if pl.current[r.Key] != nil {
				continue
			}
			parent := ""
			if r.Parent != "" {
				pr := pl.current[parentKey(r.Key)]
				if pr == nil {
					continue // The parent is created in this apply, so the child can't exist yet.
				}
				parent = pr.ID
			}
			pl.log.Debug("checking external ID is unclaimed", "resource", r.Key.String())
			if err := retrieve(ctx, pl.client, r.Key, parent); err != nil {
				return explain(err)
			}
		}
	}
	return nil
}

// checkMemoryLayerTypes catches a changed layer type. The API ignores
// spec.type on update, so the change would otherwise vanish.
func (pl *planner) checkMemoryLayerTypes(context.Context) error {
	for _, r := range pl.bundle.Sorted(config.MemoryLayer) {
		old := pl.current[r.Key]
		if old == nil {
			continue
		}
		have, ok := old.Spec.(*cadenya.MemoryLayerSpec)
		want := r.Spec.(*cadenya.MemoryLayerSpecParam).Type
		if ok && have != nil && have.Type != want {
			return fmt.Errorf("%s: a memory layer's spec.type cannot change (%s to %s); give it a new externalId to replace it", r.File, have.Type, want)
		}
	}
	return nil
}

// checkDeletes refuses deletes that would fail, or that would break
// resources outside the bundle.
func (pl *planner) checkDeletes(ctx context.Context) error {
	if len(pl.removed) == 0 {
		return nil
	}
	for _, r := range pl.removed {
		if err := checkChildren(ctx, pl.client, r, pl.key); err != nil {
			return fmt.Errorf("check delete %s: %w", r.Key, explain(err))
		}
	}
	pl.log.Debug("checking the workspace for outside references to deleted resources", "deletes", len(pl.removed))
	others, err := scanOutside(ctx, pl.client, pl.key, pl.retired)
	if err != nil {
		return explain(err)
	}
	// Cadenya won't delete a published agent's last variation. Say so now,
	// before any write, rather than after the rest of the apply.
	for _, r := range pl.bundle.Sorted(config.Agent) {
		old := pl.current[r.Key]
		if old == nil || old.State != remotePublished || r.State == config.StateDraft {
			continue
		}
		kept := others.variations[old.ID]
		for key := range pl.bundle.Resources {
			if key.Kind == config.Variation && key.Parent == r.ExternalID {
				kept++
			}
		}
		if kept == 0 {
			return fmt.Errorf("%s: this apply would delete every variation of a published agent; keep one, or set state: draft", r.File)
		}
	}
	return nil
}

// checkReferences refuses a bundle whose variations or widgets point at
// something the same apply deletes.
func (pl *planner) checkReferences(context.Context) error {
	check := func(file, verb string, targets []config.AssignmentTarget) error {
		for _, ref := range targets {
			if r := pl.retired[*ref.ID]; r != nil {
				return fmt.Errorf("%s %s %s, which this apply deletes", file, verb, r.Key)
			}
		}
		return nil
	}
	for _, r := range pl.bundle.Sorted(config.Variation) {
		if err := check(r.File, "assigns", config.AssignmentTargets(r.Spec.(*cadenya.AgentVariationSpec))); err != nil {
			return err
		}
	}
	for _, r := range pl.bundle.Sorted(config.Widget) {
		if err := check(r.File, "points at", config.WidgetTargets(r.Spec.(*cadenya.WidgetSpec))); err != nil {
			return err
		}
		// A pinned variation can't be deleted out from under its widget.
		old := pl.current[r.Key]
		if old == nil {
			continue
		}
		spec, ok := old.Spec.(*cadenya.WidgetSpec)
		if ok && spec != nil && spec.VariationID != nil && pl.retired[*spec.VariationID] != nil && !hasField(r.Mask, "spec.variationId") {
			return fmt.Errorf("%s: its pinned variation is being deleted; set spec.variationId (null to unpin)", r.File)
		}
	}
	return nil
}

// detaches returns the updates that drop assignments to deleted resources,
// from variations that outlive step 3.
func (pl *planner) detaches() ([]*Operation, error) {
	var ops []*Operation
	for _, r := range pl.bundle.Sorted(config.Variation) {
		old := pl.current[r.Key]
		if old == nil {
			continue
		}
		op, err := detach(old, pl.retired)
		if err != nil || op == nil {
			if err != nil {
				return nil, err
			}
			continue
		}
		// The update in step 4 sends the bundle's fields. If the bundle doesn't
		// set the field the detach changes, it would put the assignment back.
		for _, field := range strings.Split(op.mask, ",") {
			if !hasField(r.Mask, field) {
				return nil, fmt.Errorf("%s: include %s (or []) to remove assignments to deleted resources", r.File, field)
			}
		}
		ops = append(ops, op)
	}
	for _, r := range pl.removed {
		if r.Key.Kind != config.Variation || !pl.late(r) {
			continue
		}
		op, err := detach(r, pl.retired)
		if err != nil {
			return nil, err
		}
		if op != nil {
			ops = append(ops, op)
		}
	}
	return ops, nil
}

// transitions returns the state changes for the bundle's agents: those that
// run before the deletes (step 2), and the publishes (step 5).
func (pl *planner) transitions() (before, publishes []*Operation) {
	for _, r := range pl.bundle.Sorted(config.Agent) {
		if r.State == "" {
			continue
		}
		state, id := remoteDraft, ""
		if old := pl.current[r.Key]; old != nil {
			state, id = old.State, old.ID
		}
		if state == remoteArchived {
			before = append(before, &Operation{Action: ActionUnarchive, Key: r.Key, ID: id})
			state = remoteDraft
		}
		switch {
		case r.State == config.StateDraft && state == remotePublished:
			before = append(before, &Operation{Action: ActionUnpublish, Key: r.Key, ID: id})
		case r.State == config.StatePublished && state != remotePublished:
			publishes = append(publishes, &Operation{Action: ActionPublish, Key: r.Key, ID: id})
		}
	}
	return before, publishes
}

// unpublishRemoved unpublishes the published agents the plan deletes, right
// before their variations go.
func (pl *planner) unpublishRemoved(late bool) []*Operation {
	var ops []*Operation
	for _, r := range pl.removed {
		if r.Key.Kind == config.Agent && r.State == remotePublished && pl.late(r) == late {
			ops = append(ops, &Operation{Action: ActionUnpublish, Key: r.Key, ID: r.ID})
		}
	}
	return ops
}

func (pl *planner) deletes(late bool) []*Operation {
	var ops []*Operation
	for _, r := range pl.removed {
		if pl.late(r) == late {
			ops = append(ops, &Operation{Action: ActionDelete, Key: r.Key, ID: r.ID, remote: r})
		}
	}
	return ops
}

func (pl *planner) upserts() []*Operation {
	var ops []*Operation
	for _, kind := range config.Order {
		for _, r := range pl.bundle.Sorted(kind) {
			op := &Operation{Action: ActionCreate, Key: r.Key, File: r.File, desired: r}
			if old := pl.current[r.Key]; old != nil {
				op.Action, op.ID, op.remote = ActionUpdate, old.ID, old
			}
			ops = append(ops, op)
		}
	}
	return ops
}

// detach returns an update that drops a remote variation's assignments to
// retired resources, or nil when it has none.
func detach(old *remote, retired map[string]*remote) (*Operation, error) {
	spec, ok := old.Spec.(*cadenya.AgentVariationSpec)
	if !ok || spec == nil {
		return nil, fmt.Errorf("%s has no spec in API response", old.Key)
	}
	var fields []string
	kept := make([]cadenya.VariationAssignment, 0, len(spec.Assignments))
	for _, a := range spec.Assignments {
		if id, ok := config.AssignmentID(a); ok && retired[id] != nil {
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) != len(spec.Assignments) {
		fields = append(fields, "spec.assignments")
	}
	layers := make([]cadenya.VariationMemoryLayerAssignment, 0, len(spec.MemoryLayerAssignments))
	for _, a := range spec.MemoryLayerAssignments {
		if retired[a.MemoryLayerID] == nil {
			layers = append(layers, a)
		}
	}
	if len(layers) != len(spec.MemoryLayerAssignments) {
		fields = append(fields, "spec.memoryLayerAssignments")
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return &Operation{Action: ActionDetach, Key: old.Key, ID: old.ID, remote: old, assignments: kept, layers: layers, mask: strings.Join(fields, ",")}, nil
}

// hasField reports whether an update mask names field.
func hasField(mask, field string) bool {
	return strings.Contains(","+mask+",", ","+field+",")
}

// rank is a kind's position in dependency order.
func rank(k config.Kind) int {
	for i, v := range config.Order {
		if k == v {
			return i
		}
	}
	return -1
}
