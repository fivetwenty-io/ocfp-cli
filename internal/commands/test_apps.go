package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

const (
	// testAppKindStatic is a staticfile_buildpack app serving one page.
	testAppKindStatic = "static"

	// testAppKindProxy is a python_buildpack app that proxies /proxy to the
	// URL in its BACKEND_URL environment variable.
	testAppKindProxy = "proxy"

	// testAppDirMode is the permission mode for generated app directories.
	testAppDirMode = 0750

	// testAppFileMode is the permission mode for generated app files.
	testAppFileMode = 0600

	// testHTTPBodyLimit bounds how much of a response body a check reads.
	testHTTPBodyLimit = 64 * 1024

	// testMemoryStatic is the memory quota for the static app.
	testMemoryStatic = "64M"

	// testMemoryProxy is the memory quota for the python proxy app.
	testMemoryProxy = "128M"

	// testDiskQuota is the disk quota for every generated app.
	testDiskQuota = "256M"
)

// ErrTestUnexpectedStatus is returned when a route answers with the wrong
// HTTP status.
var ErrTestUnexpectedStatus = errors.New("unexpected HTTP status")

// ErrTestUnexpectedBody is returned when a route answers without the
// expected marker in its body.
var ErrTestUnexpectedBody = errors.New("response body is missing the expected marker")

// ErrTestNoRoute is returned when a pushed app has no route of the kind a
// check needs.
var ErrTestNoRoute = errors.New("app has no route")

// ErrTestNoLogLines is returned when cf logs --recent returns no log lines.
var ErrTestNoLogLines = errors.New("cf logs --recent returned no application log lines")

// testApp is one generated application the suites push.
type testApp struct {
	Name   string
	Kind   string
	Dir    string
	Route  string
	Marker string
	Env    map[string]string
	GUID   string
}

// testAppManifest is the cf push manifest written next to each app.
type testAppManifest struct {
	Applications []testAppEntry `yaml:"applications"`
}

// testAppEntry is one application in the manifest.
type testAppEntry struct {
	Name            string            `yaml:"name"`
	Memory          string            `yaml:"memory"`
	DiskQuota       string            `yaml:"disk_quota"`
	Instances       int               `yaml:"instances"`
	Buildpacks      []string          `yaml:"buildpacks"`
	HealthCheckType string            `yaml:"health-check-type"`
	Routes          []testAppRoute    `yaml:"routes,omitempty"`
	NoRoute         bool              `yaml:"no-route,omitempty"`
	Env             map[string]string `yaml:"env,omitempty"`
}

// testAppRoute is one route entry in the manifest.
type testAppRoute struct {
	Route string `yaml:"route"`
}

// testProxyServer is the python proxy app. It answers / with its marker and
// /proxy with whatever BACKEND_URL returns, or 502 when that fetch fails.
const testProxyServer = `import os
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(os.environ.get("PORT", "8080"))
BACKEND = os.environ.get("BACKEND_URL", "")
MARKER = os.environ.get("MARKER", "ocfp-test-proxy")


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith("/proxy"):
            try:
                with urllib.request.urlopen(BACKEND, timeout=5) as resp:
                    body = resp.read()
                    code = resp.status
            except Exception as exc:  # noqa: BLE001
                body = ("proxy to %s failed: %s" % (BACKEND, exc)).encode()
                code = 502
        else:
            body = MARKER.encode()
            code = 200
        self.send_response(code)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print("%s - %s" % (self.address_string(), fmt % args), flush=True)


HTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
`

// appName builds a unique app name for this run.
func (r *TestRunner) appName(role string) string {
	return "ocfp-test-" + r.runID + "-" + role
}

// newStaticApp generates a static app with a route on the apps domain.
func (r *TestRunner) newStaticApp(role string) (*testApp, error) {
	name := r.appName(role)

	app := &testApp{
		Name:   name,
		Kind:   testAppKindStatic,
		Dir:    "",
		Route:  name + "." + r.appsDomain,
		Marker: "ocfp-test " + name,
		Env:    nil,
		GUID:   "",
	}

	err := r.materializeApp(app)
	if err != nil {
		return nil, err
	}

	return app, nil
}

// newProxyApp generates the python proxy app pointed at backendURL.
func (r *TestRunner) newProxyApp(role, backendURL string) (*testApp, error) {
	name := r.appName(role)

	app := &testApp{
		Name:   name,
		Kind:   testAppKindProxy,
		Dir:    "",
		Route:  name + "." + r.appsDomain,
		Marker: "ocfp-test " + name,
		Env:    map[string]string{"BACKEND_URL": backendURL, "MARKER": "ocfp-test " + name},
		GUID:   "",
	}

	err := r.materializeApp(app)
	if err != nil {
		return nil, err
	}

	return app, nil
}

// materializeApp writes the app's files and manifest into a fresh temp dir.
func (r *TestRunner) materializeApp(app *testApp) error {
	dir, err := os.MkdirTemp("", "ocfp-test-app-*")
	if err != nil {
		return fmt.Errorf("failed to create app directory: %w", err)
	}

	r.mu.Lock()
	r.tempDirs = append(r.tempDirs, dir)
	r.mu.Unlock()

	app.Dir = dir

	return writeTestApp(app)
}

// writeTestApp writes the source files and manifest for app into app.Dir.
func writeTestApp(app *testApp) error {
	var err error

	switch app.Kind {
	case testAppKindStatic:
		err = writeStaticAppFiles(app.Dir, app.Marker)
	case testAppKindProxy:
		err = writeProxyAppFiles(app.Dir)
	default:
		return fmt.Errorf("unknown test app kind %q", app.Kind) //nolint:err113 // programming error
	}

	if err != nil {
		return err
	}

	manifest, err := renderAppManifest(app)
	if err != nil {
		return err
	}

	return writeAppFile(app.Dir, "manifest.yml", manifest)
}

// writeStaticAppFiles writes a staticfile_buildpack app whose page is marker.
func writeStaticAppFiles(dir, marker string) error {
	err := writeAppFile(dir, "Staticfile", []byte(""))
	if err != nil {
		return err
	}

	page := "<!doctype html><html><body><p>" + marker + "</p></body></html>\n"

	return writeAppFile(dir, "index.html", []byte(page))
}

// writeProxyAppFiles writes the python proxy app.
func writeProxyAppFiles(dir string) error {
	files := map[string]string{
		"server.py":        testProxyServer,
		"Procfile":         "web: python server.py\n",
		"requirements.txt": "",
	}

	for name, content := range files {
		err := writeAppFile(dir, name, []byte(content))
		if err != nil {
			return err
		}
	}

	return nil
}

// writeAppFile writes one file under dir.
func writeAppFile(dir, name string, content []byte) error {
	err := os.MkdirAll(dir, testAppDirMode)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}

	err = os.WriteFile(filepath.Join(dir, name), content, testAppFileMode)
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", name, err)
	}

	return nil
}

// renderAppManifest renders the cf push manifest for app.
func renderAppManifest(app *testApp) ([]byte, error) {
	entry := testAppEntry{
		Name:            app.Name,
		Memory:          testMemoryStatic,
		DiskQuota:       testDiskQuota,
		Instances:       1,
		Buildpacks:      []string{"staticfile_buildpack"},
		HealthCheckType: "port",
		Routes:          nil,
		NoRoute:         false,
		Env:             app.Env,
	}

	if app.Kind == testAppKindProxy {
		entry.Memory = testMemoryProxy
		entry.Buildpacks = []string{"python_buildpack"}
	}

	if app.Route == "" {
		entry.NoRoute = true
	} else {
		entry.Routes = []testAppRoute{{Route: app.Route}}
	}

	out, err := yaml.Marshal(testAppManifest{Applications: []testAppEntry{entry}})
	if err != nil {
		return nil, fmt.Errorf("failed to render manifest: %w", err)
	}

	return out, nil
}

// pushApp pushes the app and records its GUID.
func (r *TestRunner) pushApp(ctx context.Context, log *stepLog, app *testApp) error {
	_, err := r.cf(ctx, "push", app.Name, "-f", filepath.Join(app.Dir, "manifest.yml"), "-p", app.Dir)
	if err != nil {
		return err
	}

	app.GUID, err = r.lookupGUID(ctx, "/v3/apps", map[string]string{"names": app.Name, "space_guids": r.spaceGUID})
	if err != nil {
		return err
	}

	log.Printf("pushed %s (%s) with route %s", app.Name, app.GUID, valueOr(app.Route, "none"))

	return nil
}

// deleteApp deletes the app and its routes.
func (r *TestRunner) deleteApp(ctx context.Context, log *stepLog, app *testApp) error {
	_, err := r.cf(ctx, "delete", app.Name, "-f", "-r")
	if err != nil {
		return err
	}

	log.Printf("deleted %s", app.Name)

	return nil
}

// restartApp restarts the app and confirms cf reports it running.
func (r *TestRunner) restartApp(ctx context.Context, log *stepLog, app *testApp) error {
	out, err := r.cf(ctx, "restart", app.Name)
	if err != nil {
		return err
	}

	log.Printf("restarted %s: %s", app.Name, lastLines(out, 1))

	return nil
}

// appRoutes lists the routes mapped to the app.
func (r *TestRunner) appRoutes(ctx context.Context, app *testApp) ([]ccRoute, error) {
	resources, err := r.cfCurlAll(ctx, ccQuery("/v3/apps/"+app.GUID+"/routes", nil))
	if err != nil {
		return nil, err
	}

	routes := make([]ccRoute, 0, len(resources))

	for _, raw := range resources {
		var route ccRoute

		err := json.Unmarshal(raw, &route)
		if err != nil {
			return nil, fmt.Errorf("decoding route: %w", err)
		}

		routes = append(routes, route)
	}

	return routes, nil
}

// appURL is the HTTPS URL of the app's route.
func (app *testApp) appURL(path string) string {
	return "https://" + app.Route + path
}

// httpGet fetches url and returns the status and the start of the body.
func (r *TestRunner) httpGet(ctx context.Context, url string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", fmt.Errorf("building request for %s: %w", url, err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("GET %s: %w", url, err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, testHTTPBodyLimit))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("reading %s: %w", url, err)
	}

	return resp.StatusCode, string(body), nil
}

// checkHTTP fetches url once and verifies the status and marker.
func (r *TestRunner) checkHTTP(ctx context.Context, url string, wantStatus int, marker string) error {
	status, body, err := r.httpGet(ctx, url)
	if err != nil {
		return err
	}

	if status != wantStatus {
		return fmt.Errorf("%w from %s: got %d, want %d: %s", ErrTestUnexpectedStatus, url, status, wantStatus, firstLine(body))
	}

	if marker != "" && !strings.Contains(body, marker) {
		return fmt.Errorf("%w %q from %s: %s", ErrTestUnexpectedBody, marker, url, firstLine(body))
	}

	return nil
}

// waitForHTTP retries checkHTTP for a 200 until it passes or testRouteWait elapses,
// which covers route and network policy propagation.
func (r *TestRunner) waitForHTTP(ctx context.Context, log *stepLog, url, marker string) error {
	var lastErr error

	err := r.poll(ctx, r.routeWait, testRoutePollInterval, url, func(ctx context.Context) (bool, error) {
		lastErr = r.checkHTTP(ctx, url, http.StatusOK, marker)

		return lastErr == nil, nil
	})
	if err != nil {
		if lastErr != nil {
			return fmt.Errorf("%w: last error: %w", err, lastErr)
		}

		return err
	}

	log.Printf("%s answered 200", url)

	return nil
}

// tcpHTTPGet opens a raw TCP connection to addr, sends an HTTP/1.1 GET, and
// returns the status line and body.
func (r *TestRunner) tcpHTTPGet(ctx context.Context, addr, host string) (int, string, error) {
	conn, err := r.dial(ctx, "tcp", addr)
	if err != nil {
		return 0, "", fmt.Errorf("dial %s: %w", addr, err)
	}

	defer func() { _ = conn.Close() }()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}

	_ = conn.SetDeadline(deadline)

	_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	if err != nil {
		return 0, "", fmt.Errorf("write to %s: %w", addr, err)
	}

	return readHTTPResponse(conn)
}

// readHTTPResponse parses an HTTP response from a raw connection.
func readHTTPResponse(conn net.Conn) (int, string, error) {
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return 0, "", fmt.Errorf("reading response: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, testHTTPBodyLimit))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("reading body: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

// waitForTCP retries a raw HTTP GET over the TCP route until the marker
// comes back.
func (r *TestRunner) waitForTCP(ctx context.Context, log *stepLog, host string, port int, marker string) error {
	addr := net.JoinHostPort(host, portString(port))

	var lastErr error

	err := r.poll(ctx, r.routeWait, testRoutePollInterval, "tcp route "+addr, func(ctx context.Context) (bool, error) {
		status, body, err := r.tcpHTTPGet(ctx, addr, host)

		switch {
		case err != nil:
			lastErr = err
		case status != http.StatusOK:
			lastErr = fmt.Errorf("%w from %s: got %d", ErrTestUnexpectedStatus, addr, status)
		case !strings.Contains(body, marker):
			lastErr = fmt.Errorf("%w %q from %s", ErrTestUnexpectedBody, marker, addr)
		default:
			return true, nil
		}

		return false, nil
	})
	if err != nil {
		if lastErr != nil {
			return fmt.Errorf("%w: last error: %w", err, lastErr)
		}

		return err
	}

	log.Printf("tcp route %s answered 200 with the app marker", addr)

	return nil
}

// valueOr returns s, or fallback when s is empty.
func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}

	return s
}
