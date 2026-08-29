package app

import (
	"encoding/json"
	"testing"

	"github.com/kilo666mj/rendercase/internal/store"
)

func TestAudienceAllowed(t *testing.T) {
	allowed := []string{"https://rendercase.example/mcp", "http://127.0.0.1:18101/mcp"}
	for _, test := range []struct {
		name      string
		audiences []string
		permitted bool
	}{
		{"public", []string{"https://rendercase.example/mcp"}, true},
		{"loopback", []string{"http://127.0.0.1:18101/mcp"}, true},
		{"one matching", []string{"unrelated", "https://rendercase.example/mcp"}, true},
		{"unrelated", []string{"https://other.example/mcp"}, false},
		{"empty", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := audienceAllowed(test.audiences, allowed); got != test.permitted {
				t.Fatalf("audienceAllowed() = %v, want %v", got, test.permitted)
			}
		})
	}
}

func TestVersionForMCPUsesObjectManifest(t *testing.T) {
	v, err := versionForMCP(store.Version{
		ArtifactID: "a_test",
		Version:    1,
		Manifest:   json.RawMessage(`{"schema":"rendercase/v1","files":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Manifest["schema"]; got != "rendercase/v1" {
		t.Fatalf("manifest schema = %v", got)
	}
}
