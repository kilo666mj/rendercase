package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestRewriteOAuthMetadata(t *testing.T) {
	response := &http.Response{
		Header: http.Header{
			"Content-Type":     {"application/json"},
			"Www-Authenticate": {`Bearer resource_metadata="https://rendercase.example/.well-known/oauth-protected-resource/mcp"`},
		},
		Body:    io.NopCloser(strings.NewReader(`{"resource":"https://rendercase.example/mcp"}`)),
		Request: &http.Request{URL: mustURL(t, "https://rendercase.example/.well-known/oauth-protected-resource/mcp")},
	}
	if err := rewriteOAuthMetadata("https://rendercase.example", "http://127.0.0.1:18101")(response); err != nil {
		t.Fatal(err)
	}
	want := `Bearer resource_metadata="http://127.0.0.1:18101/.well-known/oauth-protected-resource/mcp"`
	if got := response.Header.Get("WWW-Authenticate"); got != want {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), `{"resource":"http://127.0.0.1:18101/mcp"}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestRewriteMCPUploadURL(t *testing.T) {
	response := &http.Response{
		Header:  http.Header{"Content-Type": {"text/event-stream"}},
		Body:    io.NopCloser(strings.NewReader(`{"structuredContent":{"UploadURL":"https://rendercase.example/upload/test","URL":"https://rendercase.example/a/test"}}`)),
		Request: &http.Request{URL: mustURL(t, "https://rendercase.example/mcp")},
	}
	if err := rewriteOAuthMetadata("https://rendercase.example", "http://127.0.0.1:18101")(response); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), `{"structuredContent":{"UploadURL":"http://127.0.0.1:18101/upload/test","URL":"https://rendercase.example/a/test"}}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestValidateLoopback(t *testing.T) {
	for _, test := range []struct {
		listen, public string
		valid          bool
	}{
		{"127.0.0.1:18101", "http://127.0.0.1:18101", true},
		{"[::1]:18101", "http://[::1]:18101", true},
		{"0.0.0.0:18101", "http://127.0.0.1:18101", false},
		{"127.0.0.1:18101", "https://rendercase.example", false},
	} {
		if got := validateLoopback(test.listen, test.public); (got == nil) != test.valid {
			t.Errorf("validateLoopback(%q, %q) = %v", test.listen, test.public, got)
		}
	}
}
