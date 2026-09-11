package commands

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testConfigWithFQDNs builds a bloc config with the given fqdns.base and
// explicit OCF entries.
func testConfigWithFQDNs(base string, ocf map[string]string) *config.Config {
	return &config.Config{ //nolint:exhaustruct // only the fields the test command reads
		Name:  "lab",
		FQDNs: &config.FQDNConfig{Base: base, Mgmt: map[string]string{}, OCF: ocf},
	}
}

// newRedirectingClient returns an HTTPS client that sends every request to
// server regardless of the URL's host, the way a wildcard DNS entry would.
func newRedirectingClient(server *httptest.Server) *http.Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second} //nolint:exhaustruct // defaults

	transport := &http.Transport{ //nolint:exhaustruct // defaults
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:exhaustruct,gosec // test server certificate
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, server.Listener.Addr().String())
		},
	}

	return &http.Client{Transport: transport, Timeout: 5 * time.Second} //nolint:exhaustruct // defaults
}

// hostApp returns the app name encoded in a request's Host header.
func hostApp(r *http.Request) string {
	host, _, _ := strings.Cut(r.Host, ".")

	return host
}

// markerHandler answers every request with the marker of the app named by
// the Host header.
func markerHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("<p>ocfp-test " + hostApp(r) + "</p>"))
}

// appLookup is the cf curl answer for an app named name.
func appLookup(name, guid string) string {
	return ccList(`{"guid":"` + guid + `","name":"` + name + `"}`)
}

func TestSmokeSuiteEndToEnd(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(markerHandler))
	t.Cleanup(server.Close)

	const app = "ocfp-test-abc123-smoke"

	fake := newScriptedRunner().
		on("cf curl /v3/apps?names="+app, appLookup(app, "app-guid")).
		on("cf logs "+app+" --recent", "Retrieving logs...\n2026-09-10T10:00:00.00+0000 [RTR/0] OUT GET / 200\n")
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteSmoke)
	r.httpClient = newRedirectingClient(server)

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 4, results.Passed, results.Tests)
	assert.Equal(t, 0, results.Failed)
	assert.Equal(t, 0, results.Skipped)

	assert.True(t, fake.called("cf push "+app+" -f "), "push uses the generated manifest")
	assert.True(t, fake.called("cf delete "+app+" -f -r"))

	route, ok := resultByName(results, "smoke/route_https_200")
	require.True(t, ok)
	assert.Contains(t, route.Output, "https://"+app+".apps.example.com/ answered 200")

	require.Len(t, r.tempDirs, 1)

	manifest, err := os.ReadFile(filepath.Join(r.tempDirs[0], "manifest.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "route: "+app+".apps.example.com")

	// Cleanup deletes the org and the generated directories.
	require.NoError(t, r.Cleanup(context.Background()))
	assert.True(t, fake.called("cf delete-org lab-test-org -f"))

	_, err = os.Stat(r.tempDirs[0])
	assert.True(t, os.IsNotExist(err))
}

func TestSmokeSuiteReportsRouteAndLogFailures(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	const app = "ocfp-test-abc123-smoke"

	fake := newScriptedRunner().
		on("cf curl /v3/apps?names="+app, appLookup(app, "app-guid")).
		on("cf logs "+app+" --recent", "Retrieving logs for app...\n")
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteSmoke)
	r.httpClient = newRedirectingClient(server)

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 2, results.Passed, "push and delete")
	assert.Equal(t, 2, results.Failed, "route and logs")

	route, ok := resultByName(results, "smoke/route_https_200")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, route.Status)
	assert.Contains(t, route.Error, "timed out waiting")
	assert.Contains(t, route.Error, "got 503, want 200")

	logs, ok := resultByName(results, "smoke/logs_recent")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, logs.Status)
	assert.Contains(t, logs.Error, "no application log lines")
}

func TestC2CSuiteEndToEnd(t *testing.T) {
	const (
		backend  = "ocfp-test-abc123-c2c-backend"
		frontend = "ocfp-test-abc123-c2c-frontend"
	)

	fake := newScriptedRunner().
		on("cf curl /v3/apps?names="+backend, appLookup(backend, "backend-guid")).
		on("cf curl /v3/apps?names="+frontend, appLookup(frontend, "frontend-guid"))
	installScriptedRunner(t, fake)

	// The fake frontend proxies only once the policy exists, which is what
	// the platform does once the policy server has converged.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy" {
			markerHandler(w, r)

			return
		}

		if !fake.called("cf add-network-policy " + frontend + " " + backend) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("proxy to backend failed: timed out"))

			return
		}

		_, _ = w.Write([]byte("<p>ocfp-test " + backend + "</p>"))
	}))
	t.Cleanup(server.Close)

	r := newTestRunnerForUnit(TestSuiteC2C)
	r.httpClient = newRedirectingClient(server)

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 9, results.Passed, results.Tests)
	assert.Equal(t, 0, results.Failed)
	assert.Equal(t, 0, results.Skipped)

	assert.True(t, fake.called("cf map-route "+backend+" apps.internal --hostname "+backend))
	assert.True(t, fake.called("cf add-network-policy "+frontend+" "+backend+" --protocol tcp --port 8080"))
	assert.True(t, fake.called("cf remove-network-policy "+frontend+" "+backend+" --protocol tcp --port 8080"))
	assert.True(t, fake.called("cf delete "+frontend+" -f -r"))
	assert.True(t, fake.called("cf delete "+backend+" -f -r"))

	require.Len(t, r.tempDirs, 2)

	manifest, err := os.ReadFile(filepath.Join(r.tempDirs[1], "manifest.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "BACKEND_URL: http://"+backend+".apps.internal:8080/")
	assert.Contains(t, string(manifest), "python_buildpack")
}

func TestC2CSuiteFailsWhenPoliciesAreNotEnforced(t *testing.T) {
	const (
		backend  = "ocfp-test-abc123-c2c-backend"
		frontend = "ocfp-test-abc123-c2c-frontend"
	)

	fake := newScriptedRunner().
		on("cf curl /v3/apps?names="+backend, appLookup(backend, "backend-guid")).
		on("cf curl /v3/apps?names="+frontend, appLookup(frontend, "frontend-guid"))
	installScriptedRunner(t, fake)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy" {
			_, _ = w.Write([]byte("<p>ocfp-test " + backend + "</p>"))

			return
		}

		markerHandler(w, r)
	}))
	t.Cleanup(server.Close)

	r := newTestRunnerForUnit(TestSuiteC2C)
	r.httpClient = newRedirectingClient(server)

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	denied, ok := resultByName(results, "c2c/denied_without_policy")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, denied.Status)
	assert.Contains(t, denied.Error, "without a network policy")

	fetch, ok := resultByName(results, "c2c/fetch_via_internal_route")
	require.True(t, ok)
	assert.Equal(t, TestStatusPassed, fetch.Status, "the positive path still runs from add_network_policy")
	assert.Equal(t, 1, results.Failed)
}

// blacksmithFake scripts a healthy Blacksmith lifecycle for the redis
// offering in sampleMarketplace.
func blacksmithFake(app, instance, key string) *scriptedRunner {
	return newScriptedRunner().
		on("cf curl /v3/service_brokers", ccList(brokersJSON, nfsBroker)).
		on("cf curl /v3/service_offerings", ccList(pgOffering, redisOffering, nfsOffering)).
		on("cf curl /v3/service_plans", ccList(redisAdminPlan, redisPublicPlan, pgOffPlan, nfsPlan)).
		on("cf curl /v3/apps?names="+app, appLookup(app, "app-guid")).
		on("cf curl /v3/service_instances?names="+instance,
			ccList(`{"guid":"si-guid","name":"`+instance+`","last_operation":{"type":"create","state":"in progress","description":""}}`)).
		on("cf curl /v3/service_instances?names="+instance,
			ccList(`{"guid":"si-guid","name":"`+instance+`","last_operation":{"type":"create","state":"succeeded","description":"done"}}`)).
		on("cf curl /v3/service_instances?names="+instance, ccList()).
		on("cf curl /v3/service_credential_bindings?names="+key,
			ccList(`{"guid":"key-guid","name":"`+key+`","last_operation":{"type":"create","state":"succeeded","description":""}}`)).
		on("cf curl /v3/service_credential_bindings?names="+key, ccList()).
		on("cf curl /v3/service_credential_bindings/key-guid/details", `{"credentials":{"host":"10.0.0.9","password":"s3cret","port":6379},"volume_mounts":[]}`).
		on("cf curl /v3/service_credential_bindings?app_guids=app-guid",
			ccList(`{"guid":"bind-guid","name":"","last_operation":{"type":"create","state":"succeeded","description":""}}`)).
		on("cf curl /v3/service_credential_bindings?app_guids=app-guid", ccList()).
		on("cf curl /v3/service_credential_bindings/bind-guid/details", `{"credentials":{"uri":"redis://x"},"volume_mounts":[]}`)
}

func TestBlacksmithSuiteEndToEnd(t *testing.T) {
	const (
		app      = "ocfp-test-abc123-blacksmith"
		instance = "ocfp-test-abc123-blacksmith-si"
		key      = "ocfp-test-abc123-key"
	)

	fake := blacksmithFake(app, instance, key)
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteBlacksmith)

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 9, results.Passed, results.Tests)
	assert.Equal(t, 0, results.Failed)
	assert.Equal(t, 0, results.Skipped)

	assert.True(t, fake.called("cf create-service redis standalone "+instance+" -b blacksmith"))
	assert.False(t, fake.called("cf enable-service-access"), "public plan needs no access change")
	assert.True(t, fake.called("cf create-service-key "+instance+" "+key))
	assert.True(t, fake.called("cf bind-service "+app+" "+instance))
	assert.True(t, fake.called("cf unbind-service "+app+" "+instance))
	assert.True(t, fake.called("cf delete-service-key "+instance+" "+key+" -f"))
	assert.True(t, fake.called("cf delete-service "+instance+" -f"))
	assert.True(t, fake.called("cf delete "+app+" -f -r"))

	find, ok := resultByName(results, "blacksmith/find_offering")
	require.True(t, ok)
	assert.Contains(t, find.Output, "selected redis/standalone from broker blacksmith")

	keyResult, ok := resultByName(results, "blacksmith/create_service_key")
	require.True(t, ok)
	assert.Contains(t, keyResult.Output, "carries credentials: host, password, port")
	assert.NotContains(t, keyResult.Output, "s3cret")
}

func TestBlacksmithSuiteEnablesAccessForExplicitAdminPlan(t *testing.T) {
	const (
		app      = "ocfp-test-abc123-blacksmith"
		instance = "ocfp-test-abc123-blacksmith-si"
		key      = "ocfp-test-abc123-key"
	)

	fake := blacksmithFake(app, instance, key)
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteBlacksmith)
	r.Offering = "redis"
	r.Plan = "cluster"

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 0, results.Failed, results.Tests)
	assert.True(t, fake.called("cf enable-service-access redis -b blacksmith -p cluster -o lab-test-org"))
	assert.True(t, fake.called("cf create-service redis cluster "+instance+" -b blacksmith"))
}

func TestBlacksmithSuiteFailedCreateStillCleansUp(t *testing.T) {
	const (
		app      = "ocfp-test-abc123-blacksmith"
		instance = "ocfp-test-abc123-blacksmith-si"
	)

	fake := newScriptedRunner().
		on("cf curl /v3/service_brokers", ccList(brokersJSON)).
		on("cf curl /v3/service_offerings", ccList(redisOffering)).
		on("cf curl /v3/service_plans", ccList(redisPublicPlan)).
		on("cf curl /v3/apps?names="+app, appLookup(app, "app-guid")).
		on("cf curl /v3/service_instances?names="+instance,
			ccList(`{"guid":"si-guid","name":"`+instance+`","last_operation":{"type":"create","state":"failed","description":"no capacity"}}`)).
		on("cf curl /v3/service_instances?names="+instance, ccList())
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteBlacksmith)

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	create, ok := resultByName(results, "blacksmith/create_instance")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, create.Status)
	assert.Contains(t, create.Error, "no capacity")

	keyResult, ok := resultByName(results, "blacksmith/create_service_key")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, keyResult.Status)
	assert.Equal(t, "prerequisite blacksmith/create_instance did not pass", keyResult.Reason)

	deleteInstance, ok := resultByName(results, "blacksmith/delete_instance")
	require.True(t, ok)
	assert.Equal(t, TestStatusPassed, deleteInstance.Status, "cleanup runs after a failed create")
	assert.True(t, fake.called("cf delete-service "+instance+" -f"))

	deleteKey, ok := resultByName(results, "blacksmith/delete_service_key")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, deleteKey.Status)
	assert.Contains(t, deleteKey.Reason, "nothing to clean up")

	assert.Equal(t, 1, results.Failed)
}

func TestBlacksmithSuiteExplicitOfferingMissingFails(t *testing.T) {
	fake := newScriptedRunner().
		on("cf curl /v3/service_brokers", ccList(brokersJSON)).
		on("cf curl /v3/service_offerings", ccList(redisOffering)).
		on("cf curl /v3/service_plans", ccList(redisPublicPlan))
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteBlacksmith)
	r.Offering = "mysql"

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	find, ok := resultByName(results, "blacksmith/find_offering")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, find.Status)
	assert.Contains(t, find.Error, "service offering not found in marketplace: mysql")
	assert.Equal(t, 1, results.Failed)
	assert.Equal(t, 8, results.Skipped)
	assert.Equal(t, 0, results.Passed)
}

func TestVolumeSuiteSkipsWithoutShare(t *testing.T) {
	fake := newScriptedRunner().
		on("cf curl /v3/service_brokers", ccList(brokersJSON, nfsBroker)).
		on("cf curl /v3/service_offerings", ccList(redisOffering, nfsOffering)).
		on("cf curl /v3/service_plans", ccList(redisPublicPlan, nfsPlan))
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteNFS)

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	find, ok := resultByName(results, "nfs/find_broker")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, find.Status)
	assert.Equal(t, "nfs broker nfsbroker is present but --nfs-share was not given", find.Reason)

	assert.Equal(t, 0, results.Passed)
	assert.Equal(t, 0, results.Failed)
	assert.Equal(t, len(results.Tests), results.Skipped)
	assert.False(t, fake.called("cf create-service"))
	assert.False(t, fake.called("cf push"))
}

func TestVolumeSuiteEndToEnd(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(markerHandler))
	t.Cleanup(server.Close)

	const (
		app      = "ocfp-test-abc123-nfs"
		instance = "ocfp-test-abc123-nfs-si"
	)

	fake := newScriptedRunner().
		on("cf curl /v3/service_brokers", ccList(nfsBroker)).
		on("cf curl /v3/service_offerings", ccList(nfsOffering)).
		on("cf curl /v3/service_plans", ccList(nfsPlan)).
		on("cf curl /v3/apps?names="+app, appLookup(app, "app-guid")).
		on("cf curl /v3/service_instances?names="+instance,
			ccList(`{"guid":"si-guid","name":"`+instance+`","last_operation":{"type":"create","state":"succeeded","description":""}}`)).
		on("cf curl /v3/service_instances?names="+instance, ccList()).
		on("cf curl /v3/service_credential_bindings?app_guids=app-guid",
			ccList(`{"guid":"bind-guid","name":"","last_operation":{"type":"create","state":"succeeded","description":""}}`)).
		on("cf curl /v3/service_credential_bindings?app_guids=app-guid", ccList()).
		on("cf curl /v3/service_credential_bindings/bind-guid/details",
			`{"credentials":{},"volume_mounts":[{"container_dir":"/var/vcap/data/mount","device_type":"shared","mode":"rw"}]}`)
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteNFS)
	r.NFSShare = "10.0.0.5:/export/cf"
	r.httpClient = newRedirectingClient(server)

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 8, results.Passed, results.Tests)
	assert.Equal(t, 0, results.Failed)
	assert.Equal(t, 0, results.Skipped)

	assert.True(t, fake.called(`cf create-service nfs Existing `+instance+` -b nfsbroker -c {"share":"10.0.0.5:/export/cf"}`))
	assert.True(t, fake.called("cf bind-service "+app+" "+instance))
	assert.True(t, fake.called("cf restart "+app))

	bind, ok := resultByName(results, "nfs/bind_app")
	require.True(t, ok)
	assert.Contains(t, bind.Output, "1 volume mount(s)")
}

func TestVolumeSuiteBindWithoutMountsFails(t *testing.T) {
	const (
		app      = "ocfp-test-abc123-smb"
		instance = "ocfp-test-abc123-smb-si"
	)

	smbBroker := `{"guid":"b3","name":"smbbroker"}`
	smbOffering := `{"guid":"o4","name":"smb","tags":["smb"],"relationships":{"service_broker":{"data":{"guid":"b3"}}}}`
	smbPlan := `{"guid":"p5","name":"Existing","available":true,"visibility_type":"public","relationships":{"service_offering":{"data":{"guid":"o4"}}}}`

	fake := newScriptedRunner().
		on("cf curl /v3/service_brokers", ccList(smbBroker)).
		on("cf curl /v3/service_offerings", ccList(smbOffering)).
		on("cf curl /v3/service_plans", ccList(smbPlan)).
		on("cf curl /v3/apps?names="+app, appLookup(app, "app-guid")).
		on("cf curl /v3/service_instances?names="+instance,
			ccList(`{"guid":"si-guid","name":"`+instance+`","last_operation":{"type":"create","state":"succeeded","description":""}}`)).
		on("cf curl /v3/service_instances?names="+instance, ccList()).
		on("cf curl /v3/service_credential_bindings?app_guids=app-guid",
			ccList(`{"guid":"bind-guid","name":"","last_operation":{"type":"create","state":"succeeded","description":""}}`)).
		on("cf curl /v3/service_credential_bindings?app_guids=app-guid", ccList()).
		on("cf curl /v3/service_credential_bindings/bind-guid/details", `{"credentials":{},"volume_mounts":[]}`)
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteSMB)
	r.SMBShare = "//10.0.0.6/share"
	r.SMBUsername = "svc"
	r.SMBPassword = "hunter2"

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	assert.True(t, fake.called(`cf bind-service `+app+` `+instance+` -c {"password":"hunter2","username":"svc"}`))

	bind, ok := resultByName(results, "smb/bind_app")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, bind.Status)
	assert.Contains(t, bind.Error, "no volume mounts")

	restart, ok := resultByName(results, "smb/volume_mount_on_restart")
	require.True(t, ok)
	assert.Equal(t, TestStatusSkipped, restart.Status)

	unbind, ok := resultByName(results, "smb/unbind_app")
	require.True(t, ok)
	assert.Equal(t, TestStatusPassed, unbind.Status, "cleanup unbinds after a failed bind check")
}

func TestTCPSuiteEndToEnd(t *testing.T) {
	const app = "ocfp-test-abc123-tcp"

	// A plain HTTP listener stands in for the app behind the TCP router.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<p>ocfp-test " + app + "</p>"))
	}))
	t.Cleanup(server.Close)

	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)

	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	fake := newScriptedRunner().
		on("cf curl /v3/apps?names="+app, appLookup(app, "app-guid")).
		on("cf curl /v3/apps/app-guid/routes", ccList(
			`{"guid":"r1","host":"`+app+`","url":"`+app+`.apps.example.com","port":0,"protocol":"http"}`,
			`{"guid":"r2","host":"","url":"`+host+`:`+portText+`","port":`+portText+`,"protocol":"tcp"}`,
		))
	installScriptedRunner(t, fake)

	r := newTestRunnerForUnit(TestSuiteTCP)
	r.domains = append(r.domains, cfDomain{GUID: "d3", Name: host, Internal: false, Protocols: []string{"tcp"}, Shared: true})

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 5, results.Passed, results.Tests)
	assert.Equal(t, 0, results.Failed)
	assert.True(t, fake.called("cf map-route "+app+" "+host))

	mapped, ok := resultByName(results, "tcp/map_tcp_route")
	require.True(t, ok)
	assert.Contains(t, mapped.Output, "mapped tcp route "+host+":"+portText)

	connect, ok := resultByName(results, "tcp/connect_tcp_route")
	require.True(t, ok)
	assert.Contains(t, connect.Output, net.JoinHostPort(host, portString(port))+" answered 200")
}

func TestTCPSuiteSkipsWithoutDomainAndFailsOnBadOverride(t *testing.T) {
	installScriptedRunner(t, newScriptedRunner())

	r := newTestRunnerForUnit(TestSuiteTCP)

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 0, results.Passed)
	assert.Equal(t, 0, results.Failed)
	assert.Equal(t, 5, results.Skipped)

	find, ok := resultByName(results, "tcp/find_tcp_domain")
	require.True(t, ok)
	assert.Equal(t, "no TCP domain on the foundation", find.Reason)

	deleteApp, ok := resultByName(results, "tcp/delete_app")
	require.True(t, ok)
	assert.Equal(t, "nothing to clean up: tcp/push_app did not run", deleteApp.Reason)

	r = newTestRunnerForUnit(TestSuiteTCP)
	r.TCPDomain = "tcp.missing.example.com"

	results, err = r.Execute(context.Background())
	require.NoError(t, err)

	find, ok = resultByName(results, "tcp/find_tcp_domain")
	require.True(t, ok)
	assert.Equal(t, TestStatusFailed, find.Status)
	assert.Contains(t, find.Error, "TCP domain not found on the foundation: tcp.missing.example.com")
}

func TestTagsAndExcludeAcrossSuites(t *testing.T) {
	installScriptedRunner(t, newScriptedRunner())

	r := newTestRunnerForUnit(TestSuiteAll)
	r.Exclude = []string{"tcp", "smb"}
	r.Tags = []string{"find_"}

	results, err := r.Execute(context.Background())
	require.NoError(t, err)

	for _, res := range results.Tests {
		switch {
		case strings.HasPrefix(res.Name, "tcp/") || strings.HasPrefix(res.Name, "smb/"):
			if strings.Contains(res.Name, "delete") || strings.Contains(res.Name, "unbind") {
				assert.Contains(t, res.Reason, "nothing to clean up", res.Name)
			} else {
				assert.Contains(t, res.Reason, "excluded by --exclude", res.Name)
			}
		case strings.Contains(res.Name, "find_"):
			assert.NotEqual(t, "not selected by --tags", res.Reason, res.Name)
		case strings.Contains(res.Name, "delete") || strings.Contains(res.Name, "unbind") || strings.Contains(res.Name, "remove_network_policy"):
			assert.Equal(t, TestStatusSkipped, res.Status, res.Name)
		default:
			assert.Equal(t, "not selected by --tags", res.Reason, res.Name)
		}
	}
}

func TestValidateEnvironmentAndSetup(t *testing.T) {
	t.Setenv("CF_HOME", t.TempDir())

	require.NoError(t, os.MkdirAll(filepath.Join(os.Getenv("CF_HOME"), ".cf"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("CF_HOME"), ".cf", "config.json"), []byte(`{"SSLDisabled":true}`), 0o600))

	fake := newScriptedRunner().
		on("cf api", "API endpoint:   https://api.other.example.com\n").
		on("cf target", "API endpoint:   https://api.system.ocf.example.com\nuser:           admin\n").
		on("cf curl /v3/organizations?names=lab-test-org", ccList(`{"guid":"org-guid","name":"lab-test-org"}`)).
		on("cf curl /v3/spaces?names=lab-test-space", ccList(`{"guid":"space-guid","name":"lab-test-space"}`)).
		on("cf curl /v3/domains", ccList(
			`{"guid":"d1","name":"apps.ocf.example.com","internal":false,"supported_protocols":["http"],"relationships":{"organization":{"data":null}}}`,
			`{"guid":"d2","name":"apps.internal","internal":true,"supported_protocols":["http"],"relationships":{"organization":{"data":null}}}`,
		)).
		on("cf curl /v3/organizations/org-guid/domains/default", `{"guid":"d1","name":"apps.ocf.example.com"}`)
	installScriptedRunner(t, fake)

	r := createTestRunner(testConfigWithFQDNs("ocf.example.com", nil), TestSuiteSmoke, &testOptions{}) //nolint:exhaustruct // defaults
	r.out = os.Stdout

	require.NoError(t, r.ValidateEnvironment(context.Background()))
	assert.True(t, r.SkipSSLValidation, "read from the cf CLI config")
	assert.Equal(t, "https://api.system.ocf.example.com", r.apiURL)
	assert.True(t, fake.called("cf api https://api.system.ocf.example.com --skip-ssl-validation"))
	assert.NotEmpty(t, r.runID)

	require.NoError(t, r.Setup(context.Background()))
	assert.Equal(t, "lab-test-org", r.orgName)
	assert.Equal(t, "org-guid", r.orgGUID)
	assert.Equal(t, "space-guid", r.spaceGUID)
	assert.Equal(t, "apps.ocf.example.com", r.appsDomain)
	assert.Len(t, r.domains, 2)
	assert.True(t, fake.called("cf create-org lab-test-org"))
	assert.True(t, fake.called("cf create-space lab-test-space -o lab-test-org"))
	assert.True(t, fake.called("cf target -o lab-test-org -s lab-test-space"))
}

func TestValidateEnvironmentRequiresCFAndLogin(t *testing.T) {
	t.Setenv("CF_HOME", t.TempDir())

	missing := newScriptedRunner()
	missing.missing["cf"] = true
	installScriptedRunner(t, missing)

	r := createTestRunner(testConfigWithFQDNs("ocf.example.com", nil), TestSuiteSmoke, &testOptions{}) //nolint:exhaustruct // defaults
	r.out = os.Stdout

	err := r.ValidateEnvironment(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cf CLI is required")

	loggedOut := newScriptedRunner().
		on("cf api", "API endpoint:   https://api.system.ocf.example.com\n").
		fail("cf target", "Not logged in. Use 'cf login' or 'cf login --sso' to log in.\nFAILED")
	installScriptedRunner(t, loggedOut)

	r = createTestRunner(testConfigWithFQDNs("ocf.example.com", nil), TestSuiteSmoke, &testOptions{}) //nolint:exhaustruct // defaults
	r.out = os.Stdout

	err = r.ValidateEnvironment(context.Background())
	require.ErrorIs(t, err, ErrTestNotLoggedIn)
	assert.False(t, loggedOut.called("cf api https://"), "matching target is left alone")
}
