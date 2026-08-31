package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kilo666mj/rendercase/internal/store"
)

func TestAdminMCPToolsAreVisibleOnlyToAdministrators(t *testing.T) {
	for _, test := range []struct {
		name  string
		admin bool
	}{
		{name: "ordinary user", admin: false},
		{name: "administrator", admin: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := (&Server{}).newMCPServer(store.User{ID: "user", Admin: test.admin})
			clientTransport, serverTransport := mcp.NewInMemoryTransports()
			serverSession, err := server.Connect(context.Background(), serverTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer serverSession.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			clientSession, err := client.Connect(context.Background(), clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer clientSession.Close()
			result, err := clientSession.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			seenAdmin := false
			for _, tool := range result.Tools {
				if strings.HasPrefix(tool.Name, "rendercase_admin_") {
					seenAdmin = true
				}
			}
			if seenAdmin != test.admin {
				t.Fatalf("admin tools visible = %v, want %v", seenAdmin, test.admin)
			}
		})
	}
}

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
