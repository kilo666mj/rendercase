package main

import (
	"net/http"
	"testing"
)

func TestConfiguredHTTPClientDefaults(t *testing.T) {
	t.Setenv("RENDERCASE_CLIENT_CERT", "")
	t.Setenv("RENDERCASE_CLIENT_KEY", "")
	t.Setenv("RENDERCASE_CA_FILE", "")
	client, err := configuredHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		t.Fatal("configured client is missing its TLS transport")
	}
}

func TestConfiguredHTTPClientRequiresCertificatePair(t *testing.T) {
	t.Setenv("RENDERCASE_CLIENT_CERT", "/tmp/client.crt")
	t.Setenv("RENDERCASE_CLIENT_KEY", "")
	if _, err := configuredHTTPClient(); err == nil {
		t.Fatal("expected a partial client identity to be rejected")
	}
}
