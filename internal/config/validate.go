package config

import (
	"fmt"
	"regexp"
	"strings"

	cadenya "go.cadenya.com/cadenya-go"
)

var labelValue = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)

// ValidateBundleKey checks that value can be a label value.
func ValidateBundleKey(value string) error {
	if !labelValue.MatchString(value) {
		return fmt.Errorf("bundle key must be 1 to 63 characters: letters, digits, '.', '_', or '-', starting and ending with a letter or digit")
	}
	return nil
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
