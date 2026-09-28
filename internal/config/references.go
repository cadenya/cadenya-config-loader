package config

import (
	"fmt"
	"strings"

	cadenya "go.cadenya.com/cadenya-go"
)

// AssignmentTarget points to a mutable target ID within an SDK union.
type AssignmentTarget struct {
	Kind Kind
	ID   *string
}

// AssignmentTargets exposes SDK union targets without losing their variant.
func AssignmentTargets(spec *cadenya.AgentVariationSpec) []AssignmentTarget {
	var result []AssignmentTarget
	for i := range spec.Assignments {
		a := &spec.Assignments[i]
		switch {
		case a.ToolSetID != nil:
			result = append(result, AssignmentTarget{ToolSet, &a.ToolSetID.ToolSetID})
		case a.ToolID != nil:
			result = append(result, AssignmentTarget{Tool, &a.ToolID.ToolID})
		case a.SubAgentID != nil:
			result = append(result, AssignmentTarget{Agent, &a.SubAgentID.SubAgentID})
		}
	}
	for i := range spec.MemoryLayerAssignments {
		result = append(result, AssignmentTarget{MemoryLayer, &spec.MemoryLayerAssignments[i].MemoryLayerID})
	}
	return result
}

// WidgetTargets exposes a widget's agent and pinned variation references.
func WidgetTargets(spec *cadenya.WidgetSpec) []AssignmentTarget {
	result := []AssignmentTarget{{Agent, &spec.AgentID}}
	if spec.VariationID != nil {
		result = append(result, AssignmentTarget{Variation, spec.VariationID})
	}
	return result
}

func ReferenceKey(kind Kind, value string) (Key, bool, error) {
	if !strings.HasPrefix(value, "external_id:") {
		return Key{}, false, nil
	}
	id := strings.TrimPrefix(value, "external_id:")
	key := Key{Kind: kind, ExternalID: id}
	if kind == Tool || kind == Variation {
		parts := strings.Split(id, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			if kind == Variation {
				return Key{}, false, fmt.Errorf("variation references must use external_id:<agent-external-id>/<variation-external-id>")
			}
			return Key{}, false, fmt.Errorf("tool references must use external_id:<tool-set-external-id>/<tool-external-id>")
		}
		key.Parent, key.ExternalID = parts[0], parts[1]
	}
	return key, true, nil
}

func (b *Bundle) ValidateReferences() error {
	for _, r := range b.Sorted(Variation) {
		for _, ref := range AssignmentTargets(r.Spec.(*cadenya.AgentVariationSpec)) {
			if strings.TrimSpace(*ref.ID) == "" {
				return fmt.Errorf("%s: assignment target must not be blank", r.File)
			}
			key, local, err := ReferenceKey(ref.Kind, *ref.ID)
			if err != nil {
				return fmt.Errorf("%s: %w", r.File, err)
			}
			if local && b.Resources[key] == nil {
				return fmt.Errorf("%s: assignment references missing local %s", r.File, key)
			}
		}
	}
	for _, r := range b.Sorted(Widget) {
		spec := r.Spec.(*cadenya.WidgetSpec)
		agent, agentLocal, err := ReferenceKey(Agent, spec.AgentID)
		if err != nil {
			return fmt.Errorf("%s: %w", r.File, err)
		}
		if agentLocal && b.Resources[agent] == nil {
			return fmt.Errorf("%s: spec.agentId references missing local %s", r.File, agent)
		}
		if spec.VariationID == nil {
			continue
		}
		variation, local, err := ReferenceKey(Variation, *spec.VariationID)
		if err != nil {
			return fmt.Errorf("%s: spec.variationId: %w", r.File, err)
		}
		if !local {
			continue
		}
		if b.Resources[variation] == nil {
			return fmt.Errorf("%s: spec.variationId references missing local %s", r.File, variation)
		}
		// The API requires the pinned variation to belong to the widget's agent.
		if !agentLocal || variation.Parent != agent.ExternalID {
			return fmt.Errorf("%s: spec.variationId must be a variation of spec.agentId", r.File)
		}
	}
	return nil
}
