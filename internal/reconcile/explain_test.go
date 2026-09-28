package reconcile

import (
	"errors"
	"fmt"
	"testing"

	cadenya "go.cadenya.com/cadenya-go"
)

func TestExplainAddsAPIDetails(t *testing.T) {
	violation := &cadenya.APIError{StatusCode: 400, Code: 3, Message: "validation failed", Details: []map[string]any{{
		"@type":           "type.googleapis.com/google.rpc.BadRequest",
		"fieldViolations": []any{map[string]any{"field": "spec.model_config.model_id", "description": "model_id must be a reference key"}},
	}}}
	scope := &cadenya.APIError{StatusCode: 403, Code: 7, Message: "caller does not hold the scope", Details: []map[string]any{{
		"@type":    "type.googleapis.com/google.rpc.ErrorInfo",
		"reason":   "SCOPE_MISSING",
		"metadata": map[string]any{"required_scope": "memory:manage", "granted_scopes": "agents:manage"},
	}}}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("create agentVariation a/b: %w", violation), "create agentVariation a/b: cadenya: validation failed (http 400, code 3): spec.model_config.model_id: model_id must be a reference key"},
		{scope, "cadenya: caller does not hold the scope (http 403, code 7): SCOPE_MISSING (granted_scopes=agents:manage, required_scope=memory:manage)"},
		{&cadenya.APIError{StatusCode: 404, Code: 5, Message: "not found"}, "cadenya: not found (http 404, code 5)"},
		{errors.New("plain"), "plain"},
	} {
		got := explain(tc.err)
		if got.Error() != tc.want {
			t.Errorf("got  %q\nwant %q", got.Error(), tc.want)
		}
		var apiErr *cadenya.APIError
		if errors.As(tc.err, &apiErr) && !errors.As(got, &apiErr) {
			t.Error("explain lost the API error")
		}
	}
}
