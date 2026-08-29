package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	listen := env("RENDERCASE_PROXY_LISTEN", "127.0.0.1:18101")
	localURL := env("RENDERCASE_PROXY_URL", "http://127.0.0.1:18101")
	upstream := env("RENDERCASE_URL", "https://rendercase.example.com")
	certFile := os.Getenv("RENDERCASE_CLIENT_CERT")
	keyFile := os.Getenv("RENDERCASE_CLIENT_KEY")

	if err := validateLoopback(listen, localURL); err != nil {
		log.Fatal(err)
	}
	if certFile == "" || keyFile == "" {
		log.Fatal("RENDERCASE_CLIENT_CERT and RENDERCASE_CLIENT_KEY are required")
	}
	target, err := url.Parse(upstream)
	if err != nil || target.Scheme != "https" || target.Host == "" {
		log.Fatal("RENDERCASE_URL must be an absolute HTTPS URL")
	}
	identity, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatalf("load client identity: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Host = target.Host
		// Let Go negotiate gzip itself so ModifyResponse always receives decoded
		// OAuth metadata, even when the downstream client advertises Brotli.
		request.Header.Del("Accept-Encoding")
	}
	proxy.Transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{identity}},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	proxy.ModifyResponse = rewriteOAuthMetadata(upstream, localURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, problem error) {
		log.Printf("upstream request failed: %v", problem)
		http.Error(w, "Rendercase bridge unavailable", http.StatusBadGateway)
	}

	server := &http.Server{
		Addr:              listen,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	log.Printf("Rendercase mTLS bridge listening on %s", localURL)
	log.Fatal(server.ListenAndServe())
}

func rewriteOAuthMetadata(upstream, local string) func(*http.Response) error {
	upstream = strings.TrimRight(upstream, "/")
	local = strings.TrimRight(local, "/")
	return func(response *http.Response) error {
		for _, header := range []string{"WWW-Authenticate", "Link", "Location"} {
			values := response.Header.Values(header)
			if len(values) == 0 {
				continue
			}
			response.Header.Del(header)
			for _, value := range values {
				response.Header.Add(header, strings.ReplaceAll(value, upstream+"/.well-known/oauth-protected-resource", local+"/.well-known/oauth-protected-resource"))
			}
		}
		if response.Request != nil && strings.HasPrefix(response.Request.URL.Path, "/.well-known/oauth-protected-resource") && strings.Contains(response.Header.Get("Content-Type"), "application/json") {
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			var metadata map[string]any
			if err = json.Unmarshal(body, &metadata); err != nil {
				return err
			}
			metadata["resource"] = local + "/mcp"
			body, err = json.Marshal(metadata)
			if err != nil {
				return err
			}
			setResponseBody(response, body)
		}
		// MCP upload URLs use the canonical public origin. Clients connected
		// through this bridge must PUT through the bridge too so the upload
		// carries the same mTLS identity.
		contentType := response.Header.Get("Content-Type")
		if response.Request != nil && response.Request.URL.Path == "/mcp" && (strings.Contains(contentType, "application/json") || strings.Contains(contentType, "text/event-stream")) {
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			for _, field := range []string{"UploadURL", "upload_url"} {
				body = bytes.ReplaceAll(body, []byte(`"`+field+`":"`+upstream), []byte(`"`+field+`":"`+local))
			}
			setResponseBody(response, body)
		}
		return nil
	}
}

func setResponseBody(response *http.Response, body []byte) {
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", fmt.Sprint(len(body)))
}

func validateLoopback(listen, localURL string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid RENDERCASE_PROXY_LISTEN: %w", err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("Rendercase bridge must listen on a loopback address")
		}
	}
	u, err := url.Parse(localURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return errors.New("RENDERCASE_PROXY_URL must be an absolute loopback HTTP URL")
	}
	localHost := u.Hostname()
	if localHost != "localhost" {
		ip := net.ParseIP(localHost)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("RENDERCASE_PROXY_URL must use a loopback host")
		}
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
