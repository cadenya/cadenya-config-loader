package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cadenya/cadenya-config-loader/internal/config"
	cadenya "go.cadenya.com/cadenya-go"
)

// Apply runs the plan's operations in order and stops at the first error.
// Each operation's Completed field records how far it got. A plan runs once:
// build a fresh one before retrying a failed apply.
func (p *Plan) Apply(ctx context.Context) error {
	if p.Applied {
		return errors.New("plan has already been applied; build a fresh plan")
	}
	for _, op := range p.Operations {
		if op.Completed {
			return errors.New("plan was partially applied; build a fresh plan")
		}
	}
	// Canonical IDs by key, including the ones this apply creates, so later
	// operations can resolve references to earlier ones.
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
		err := p.run(ctx, op, ids)
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

// run performs one operation and records the IDs it creates or removes.
func (p *Plan) run(ctx context.Context, op *Operation, ids map[config.Key]string) error {
	switch op.Action {
	case ActionDetach:
		spec := &cadenya.AgentVariationSpec{Assignments: op.assignments, MemoryLayerAssignments: op.layers}
		_, err := p.client.Agents().Variations().Update(ctx, op.remote.ParentID, op.ID, &cadenya.AgentVariationUpdateParams{Spec: spec, UpdateMask: &op.mask})
		return err
	case ActionDelete:
		if err := deleteRemote(ctx, p.client, op.remote); err != nil {
			return err
		}
		delete(ids, op.Key)
		return nil
	case ActionPublish, ActionUnpublish, ActionUnarchive:
		if op.ID == "" {
			op.ID = ids[op.Key] // The agent was created earlier in this apply.
		}
		if op.ID == "" {
			return errors.New("agent was not resolved")
		}
		return transition(ctx, p.client, op.Action, op.ID)
	case ActionCreate, ActionUpdate:
		parent := ""
		if op.Parent != "" {
			if parent = ids[parentKey(op.Key)]; parent == "" {
				return errors.New("parent was not resolved")
			}
		}
		r, err := resolve(op.desired, ids)
		if err != nil {
			return err
		}
		id, host, err := upsert(ctx, p.client, r, op.remote, parent)
		if err != nil {
			return err
		}
		ids[op.Key] = id
		op.ID, op.Host = id, host
		return nil
	}
	return fmt.Errorf("unknown action %q", op.Action)
}

// resolve returns r with local external_id references in a variation or widget
// replaced by canonical IDs. It copies first, so the loaded bundle stays reusable.
func resolve(r *config.Resource, ids map[config.Key]string) (*config.Resource, error) {
	var targets []config.AssignmentTarget
	resolved := *r
	switch spec := r.Spec.(type) {
	case *cadenya.AgentVariationSpec:
		clone := *spec
		clone.Assignments = make([]cadenya.VariationAssignment, len(spec.Assignments))
		for i, a := range spec.Assignments {
			clone.Assignments[i] = cloneAssignment(a)
		}
		clone.MemoryLayerAssignments = append([]cadenya.VariationMemoryLayerAssignment(nil), spec.MemoryLayerAssignments...)
		resolved.Spec = &clone
		targets = config.AssignmentTargets(&clone)
	case *cadenya.WidgetSpec:
		clone := *spec
		if spec.VariationID != nil {
			v := *spec.VariationID
			clone.VariationID = &v
		}
		resolved.Spec = &clone
		targets = config.WidgetTargets(&clone)
	default:
		return r, nil
	}
	for _, ref := range targets {
		key, local, err := config.ReferenceKey(ref.Kind, *ref.ID)
		if err != nil {
			return nil, err
		}
		if !local {
			continue
		}
		if ids[key] == "" {
			return nil, fmt.Errorf("unresolved %s", key)
		}
		*ref.ID = ids[key]
	}
	return &resolved, nil
}

// cloneAssignment copies the union variant a points to, so resolving the
// copy's references can't write through to the bundle.
func cloneAssignment(a cadenya.VariationAssignment) cadenya.VariationAssignment {
	if a.ToolID != nil {
		v := *a.ToolID
		a.ToolID = &v
	}
	if a.ToolSetID != nil {
		v := *a.ToolSetID
		a.ToolSetID = &v
	}
	if a.SubAgentID != nil {
		v := *a.SubAgentID
		a.SubAgentID = &v
	}
	if a.AgentPoolID != nil {
		v := *a.AgentPoolID
		a.AgentPoolID = &v
	}
	return a
}
