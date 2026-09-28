package reconcile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cadenya/cadenya-config-loader/internal/config"
	cadenya "go.cadenya.com/cadenya-go"
)

func TestDeleteTreatsNotFoundAsDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		w.Write([]byte(`{"code":5,"message":"not found"}`))
	}))
	defer server.Close()
	c, err := cadenya.NewClient(cadenya.WithAPIKey("k"), cadenya.WithBaseURL(server.URL), cadenya.WithWorkspaceID("w"), cadenya.WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	r := &remote{Key: config.Key{Kind: config.Agent, ExternalID: "gone"}, ID: "agent_1"}
	if err := deleteRemote(context.Background(), c, r); err != nil {
		t.Fatalf("a delete that finds nothing is done: %v", err)
	}
}
