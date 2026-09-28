package reconcile

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	cadenya "go.cadenya.com/cadenya-go"
)

// detailedError adds the API's error details to an SDK error's message. The
// SDK's message stops at "validation failed (http 400, code 3)", which leaves
// out which field failed and why.
type detailedError struct {
	err     error
	details string
}

func (e detailedError) Error() string { return e.err.Error() + ": " + e.details }
func (e detailedError) Unwrap() error { return e.err }

// explain returns err with any API error details appended. Other errors pass
// through unchanged.
func explain(err error) error {
	var apiErr *cadenya.APIError
	if !errors.As(err, &apiErr) || len(apiErr.Details) == 0 {
		return err
	}
	var parts []string
	for _, detail := range apiErr.Details {
		if violations, ok := detail["fieldViolations"].([]any); ok {
			for _, v := range violations {
				m, _ := v.(map[string]any)
				field, _ := m["field"].(string)
				description, _ := m["description"].(string)
				switch {
				case field != "" && description != "":
					parts = append(parts, field+": "+description)
				case description != "":
					parts = append(parts, description)
				}
			}
		}
		if reason, ok := detail["reason"].(string); ok && reason != "" {
			part := reason
			if metadata, ok := detail["metadata"].(map[string]any); ok && len(metadata) > 0 {
				keys := make([]string, 0, len(metadata))
				for k := range metadata {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				var kv []string
				for _, k := range keys {
					kv = append(kv, fmt.Sprintf("%s=%v", k, metadata[k]))
				}
				part += " (" + strings.Join(kv, ", ") + ")"
			}
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return err
	}
	return detailedError{err, strings.Join(parts, "; ")}
}
