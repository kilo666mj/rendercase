package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type client struct {
	base, token string
	http        *http.Client
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	httpClient, err := configuredHTTPClient()
	fatal(err)
	c := client{base: strings.TrimRight(env("RENDERCASE_URL", "https://rendercase.example.com"), "/"), token: os.Getenv("RENDERCASE_TOKEN"), http: httpClient}
	if c.token == "" {
		fatal(errors.New("RENDERCASE_TOKEN is required"))
	}
	switch os.Args[1] {
	case "list":
		var out any
		fatal(c.json(http.MethodGet, "/api/v1/artifacts", nil, &out))
		printJSON(out)
	case "publish":
		fatal(c.publish(os.Args[2:]))
	case "share":
		fatal(c.share(os.Args[2:]))
	default:
		usage()
	}
}

func configuredHTTPClient() (*http.Client, error) {
	certFile := os.Getenv("RENDERCASE_CLIENT_CERT")
	keyFile := os.Getenv("RENDERCASE_CLIENT_KEY")
	if (certFile == "") != (keyFile == "") {
		return nil, errors.New("RENDERCASE_CLIENT_CERT and RENDERCASE_CLIENT_KEY must be set together")
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if certFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load Rendercase client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	if caFile := os.Getenv("RENDERCASE_CA_FILE"); caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read Rendercase CA file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system certificate pool: %w", err)
		}
		if !roots.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("RENDERCASE_CA_FILE contains no certificates")
		}
		tlsConfig.RootCAs = roots
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Timeout: 2 * time.Minute, Transport: transport}, nil
}

func (c client) publish(args []string) (err error) {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	title := fs.String("title", "", "artifact title")
	entry := fs.String("entrypoint", "index.html", "bundle entrypoint")
	artifact := fs.String("artifact", "", "existing artifact ID")
	zipPath := fs.String("zip", "", "ZIP bundle path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *title == "" || *zipPath == "" {
		return errors.New("--title and --zip are required")
	}
	var upload struct {
		UploadID    string `json:"upload_id"`
		UploadToken string `json:"upload_token"`
		UploadURL   string `json:"upload_url"`
	}
	if err := c.json(http.MethodPost, "/api/v1/artifacts/uploads", map[string]any{"title": *title, "entrypoint": *entry, "artifact_id": *artifact}, &upload); err != nil {
		return err
	}
	f, err := os.Open(*zipPath)
	if err != nil {
		return err
	}
	defer closeWithError(&err, "close ZIP bundle", f.Close)
	req, err := http.NewRequest(http.MethodPut, upload.UploadURL, f)
	if err != nil {
		return err
	}
	req.Header.Set("X-Rendercase-Upload-Token", upload.UploadToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer closeWithError(&err, "close upload response body", resp.Body.Close)
	if err = check(resp); err != nil {
		return err
	}
	var committed any
	if err = c.json(http.MethodPost, "/api/v1/uploads/"+upload.UploadID+"/commit", map[string]string{"upload_token": upload.UploadToken}, &committed); err != nil {
		return err
	}
	printJSON(committed)
	return nil
}
func (c client) share(args []string) error {
	fs := flag.NewFlagSet("share", flag.ContinueOnError)
	artifact := fs.String("artifact", "", "artifact ID")
	version := fs.Int("version", 0, "pinned version, or latest when omitted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *artifact == "" {
		return errors.New("--artifact is required")
	}
	body := map[string]any{}
	if *version > 0 {
		body["version"] = *version
	}
	var out any
	if err := c.json(http.MethodPost, "/api/v1/artifacts/"+*artifact+"/shares", body, &out); err != nil {
		return err
	}
	printJSON(out)
	return nil
}
func (c client) json(method, path string, input, output any) (err error) {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer closeWithError(&err, "close response body", resp.Body.Close)
	if err = check(resp); err != nil {
		return err
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}

func closeWithError(errp *error, context string, closeFn func() error) {
	if err := closeFn(); err != nil {
		*errp = errors.Join(*errp, fmt.Errorf("%s: %w", context, err))
	}
}
func check(resp *http.Response) error {
	if resp.StatusCode < 300 {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("rendercase returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
}
func printJSON(v any) { b, _ := json.MarshalIndent(v, "", "  "); fmt.Println(string(b)) }
func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "rendercase-cli:", err)
		os.Exit(1)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: rendercase-cli <list|publish|share> [options]")
	os.Exit(2)
}
