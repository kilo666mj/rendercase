package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/kilo666mj/mcpkit/mcpkittest"
	"github.com/kilo666mj/rendercase/internal/config"
	"github.com/kilo666mj/rendercase/internal/store"
)

func TestMCPTransportBoundsRequestsAndRejectsCrossOrigin(t *testing.T) {
	s := &Server{cfg: config.Config{MaxBundleBytes: 8}}
	handler, err := s.mcpHandler()
	if err != nil {
		t.Fatal(err)
	}
	limit := ((s.cfg.MaxBundleBytes + 2) / 3 * 4) + (1 << 20)
	request := httptest.NewRequest(http.MethodPost, "https://rendercase.example/mcp", strings.NewReader(strings.Repeat("x", int(limit+1))))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, store.User{ID: "user"}))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want 413; body=%q", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "https://rendercase.example/mcp", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://attacker.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request status = %d, want 403", response.Code)
	}
}

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
			clientSession := mcpkittest.Connect(t, server)
			result, err := clientSession.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{
				"rendercase_commit_upload", "rendercase_create_upload", "rendercase_get",
				"rendercase_list", "rendercase_publish", "rendercase_revoke_share", "rendercase_set_visibility", "rendercase_share",
			}
			if test.admin {
				want = append(want, "rendercase_admin_activate_branding_theme", "rendercase_admin_delete_artifact", "rendercase_admin_delete_branding_theme", "rendercase_admin_get_branding", "rendercase_admin_list", "rendercase_admin_revoke_share", "rendercase_admin_update_branding")
			}
			slices.Sort(want)
			got := make([]string, 0, len(result.Tools))
			seenAdmin := false
			for _, tool := range result.Tools {
				got = append(got, tool.Name)
				if strings.HasPrefix(tool.Name, "rendercase_admin_") {
					seenAdmin = true
				}
				if tool.Annotations == nil {
					t.Errorf("tool %q has no safety annotations", tool.Name)
					continue
				}
				readOnly := slices.Contains([]string{"rendercase_admin_get_branding", "rendercase_admin_list", "rendercase_get", "rendercase_list"}, tool.Name)
				destructive := slices.Contains([]string{"rendercase_admin_delete_artifact", "rendercase_admin_delete_branding_theme", "rendercase_admin_revoke_share", "rendercase_revoke_share"}, tool.Name)
				if tool.Annotations.ReadOnlyHint != readOnly || *tool.Annotations.DestructiveHint != destructive || *tool.Annotations.OpenWorldHint {
					t.Errorf("tool %q annotations = %+v", tool.Name, tool.Annotations)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("tool names = %v, want %v", got, want)
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
