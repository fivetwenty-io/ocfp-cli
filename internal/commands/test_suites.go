package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrTestNoInternalDomain is returned when the foundation has no internal
// domain for container-to-container routes.
var ErrTestNoInternalDomain = errors.New("no internal domain (apps.internal) on the foundation")

// ErrTestPolicyNotEnforced is returned when the frontend reaches the backend
// before any network policy exists.
var ErrTestPolicyNotEnforced = errors.New("frontend reached the backend without a network policy")

// ErrTestNoCredentials is returned when a service key carries no credentials.
var ErrTestNoCredentials = errors.New("service key carries no credentials")

// ErrTestNoVolumeMounts is returned when a volume binding carries no mounts.
var ErrTestNoVolumeMounts = errors.New("binding succeeded but carries no volume mounts")

// ErrTestTCPDomainNotFound is returned when --tcp-domain names a domain the
// foundation does not route TCP on.
var ErrTestTCPDomainNotFound = errors.New("TCP domain not found on the foundation")

// suiteState is the mutable state one suite's checks share.
type suiteState struct {
	apps           map[string]*testApp
	offering       *marketplaceOffering
	plan           *marketplacePlan
	instance       ccServiceInstance
	keyName        string
	internalDomain string
	backendURL     string
	tcpDomain      cfDomain
	tcpPort        int
}

func newSuiteState() *suiteState {
	return &suiteState{ //nolint:exhaustruct // populated as checks run
		apps: map[string]*testApp{},
	}
}

// stepName builds the <suite>/<check> name.
func stepName(suite TestSuite, check string) string {
	return string(suite) + "/" + check
}

// suiteSteps returns the ordered checks for one concrete suite.
func (r *TestRunner) suiteSteps(suite TestSuite) []testStep {
	st := newSuiteState()

	switch suite {
	case TestSuiteSmoke:
		return r.smokeSteps(suite, st)
	case TestSuiteC2C:
		return r.c2cSteps(suite, st)
	case TestSuiteBlacksmith:
		return r.blacksmithSteps(suite, st)
	case TestSuiteNFS, TestSuiteSMB:
		return r.volumeSteps(suite, st)
	case TestSuiteTCP:
		return r.tcpSteps(suite, st)
	case TestSuiteAcceptance, TestSuiteAll:
		return nil
	default:
		return nil
	}
}

// pushStep pushes a static app under role and stores it in st.apps.
func (r *TestRunner) pushStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		app, err := r.newStaticApp(role)
		if err != nil {
			return "", err
		}

		st.apps[role] = app

		return "", r.pushApp(ctx, log, app)
	}
}

// deleteStep deletes the apps stored under roles.
func (r *TestRunner) deleteStep(st *suiteState, roles ...string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		var errs []error

		for _, role := range roles {
			app, ok := st.apps[role]
			if !ok {
				continue
			}

			errs = append(errs, r.deleteApp(ctx, log, app))
		}

		return "", errors.Join(errs...)
	}
}

// routeStep confirms the app's HTTPS route answers 200 with its marker.
func (r *TestRunner) routeStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		app := st.apps[role]

		return "", r.waitForHTTP(ctx, log, app.appURL("/"), app.Marker)
	}
}

// smokeSteps pushes a static app, checks its route and logs, and deletes it.
func (r *TestRunner) smokeSteps(suite TestSuite, st *suiteState) []testStep {
	const role = "smoke"

	push := stepName(suite, "push_app")

	return []testStep{
		{Name: push, Run: r.pushStep(st, role), Needs: nil, Cleanup: false},
		{Name: stepName(suite, "route_https_200"), Run: r.routeStep(st, role), Needs: []string{push}, Cleanup: false},
		{Name: stepName(suite, "logs_recent"), Run: r.logsStep(st, role), Needs: []string{push}, Cleanup: false},
		{Name: stepName(suite, "delete_app"), Run: r.deleteStep(st, role), Needs: []string{push}, Cleanup: true},
	}
}

// logsStep proves the log path with cf logs --recent.
func (r *TestRunner) logsStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		app := st.apps[role]

		out, err := r.cf(ctx, "logs", app.Name, "--recent")
		if err != nil {
			return "", err
		}

		lines := countLogLines(out)
		if lines == 0 {
			return "", fmt.Errorf("%w for %s: %s", ErrTestNoLogLines, app.Name, lastLines(out, 3))
		}

		log.Printf("cf logs --recent returned %d log lines for %s", lines, app.Name)

		return "", nil
	}
}

// c2cSteps proves container-to-container networking end to end.
func (r *TestRunner) c2cSteps(suite TestSuite, st *suiteState) []testStep {
	const (
		backend  = "c2c-backend"
		frontend = "c2c-frontend"
	)

	findDomain := stepName(suite, "find_internal_domain")
	pushBackend := stepName(suite, "push_backend")
	mapInternal := stepName(suite, "map_internal_route")
	pushFrontend := stepName(suite, "push_frontend")
	addPolicy := stepName(suite, "add_network_policy")

	return []testStep{
		{Name: findDomain, Run: r.findInternalDomainStep(st), Needs: nil, Cleanup: false},
		{Name: pushBackend, Run: r.pushStep(st, backend), Needs: []string{findDomain}, Cleanup: false},
		{Name: mapInternal, Run: r.mapInternalRouteStep(st, backend), Needs: []string{pushBackend}, Cleanup: false},
		{Name: pushFrontend, Run: r.pushFrontendStep(st, frontend), Needs: []string{mapInternal}, Cleanup: false},
		{Name: stepName(suite, "denied_without_policy"), Run: r.deniedWithoutPolicyStep(st, frontend), Needs: []string{pushFrontend}, Cleanup: false},
		{Name: addPolicy, Run: r.networkPolicyStep(st, frontend, backend, "add-network-policy"), Needs: []string{pushFrontend}, Cleanup: false},
		{Name: stepName(suite, "fetch_via_internal_route"), Run: r.fetchViaInternalRouteStep(st, frontend, backend), Needs: []string{addPolicy}, Cleanup: false},
		{Name: stepName(suite, "remove_network_policy"), Run: r.networkPolicyStep(st, frontend, backend, "remove-network-policy"), Needs: []string{addPolicy}, Cleanup: true},
		{Name: stepName(suite, "delete_apps"), Run: r.deleteStep(st, frontend, backend), Needs: []string{pushBackend}, Cleanup: true},
	}
}

func (r *TestRunner) findInternalDomainStep(st *suiteState) stepFunc {
	return func(_ context.Context, log *stepLog) (string, error) {
		st.internalDomain = findInternalDomain(r.domains)
		if st.internalDomain == "" {
			return "", ErrTestNoInternalDomain
		}

		log.Printf("internal domain %s", st.internalDomain)

		return "", nil
	}
}

func (r *TestRunner) mapInternalRouteStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		app := st.apps[role]

		_, err := r.cf(ctx, "map-route", app.Name, st.internalDomain, "--hostname", app.Name)
		if err != nil {
			return "", err
		}

		st.backendURL = fmt.Sprintf("http://%s.%s:%d/", app.Name, st.internalDomain, testAppPort)

		log.Printf("mapped internal route %s", st.backendURL)

		return "", nil
	}
}

func (r *TestRunner) pushFrontendStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		app, err := r.newProxyApp(role, st.backendURL)
		if err != nil {
			return "", err
		}

		st.apps[role] = app

		err = r.pushApp(ctx, log, app)
		if err != nil {
			return "", err
		}

		return "", r.waitForHTTP(ctx, log, app.appURL("/"), app.Marker)
	}
}

// deniedWithoutPolicyStep confirms the frontend gets a 502 from its proxy
// path before any policy exists. A 200 means policies are not enforced.
func (r *TestRunner) deniedWithoutPolicyStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		url := st.apps[role].appURL("/proxy")

		status, body, err := r.httpGet(ctx, url)
		if err != nil {
			return "", err
		}

		switch status {
		case http.StatusBadGateway:
			log.Printf("%s answered 502 before the policy: %s", url, firstLine(body))

			return "", nil
		case http.StatusOK:
			return "", fmt.Errorf("%w: %s answered 200", ErrTestPolicyNotEnforced, url)
		default:
			return "", fmt.Errorf("%w from %s: got %d, want 502: %s", ErrTestUnexpectedStatus, url, status, firstLine(body))
		}
	}
}

func (r *TestRunner) networkPolicyStep(st *suiteState, source, destination, command string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		src, dst := st.apps[source], st.apps[destination]
		if src == nil || dst == nil {
			return "", nil
		}

		_, err := r.cf(ctx, command, src.Name, dst.Name, "--protocol", "tcp", "--port", portString(testAppPort))
		if err != nil {
			return "", err
		}

		log.Printf("%s %s -> %s tcp/%d", command, src.Name, dst.Name, testAppPort)

		return "", nil
	}
}

func (r *TestRunner) fetchViaInternalRouteStep(st *suiteState, source, destination string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		url := st.apps[source].appURL("/proxy")

		return "", r.waitForHTTP(ctx, log, url, st.apps[destination].Marker)
	}
}

// blacksmithSteps exercises a Blacksmith offering through the full service
// instance lifecycle.
func (r *TestRunner) blacksmithSteps(suite TestSuite, st *suiteState) []testStep {
	const role = "blacksmith"

	find := stepName(suite, "find_offering")
	push := stepName(suite, "push_app")
	create := stepName(suite, "create_instance")
	key := stepName(suite, "create_service_key")
	bind := stepName(suite, "bind_app")

	return []testStep{
		{Name: find, Run: r.findOfferingStep(st, r.Offering, r.Plan, "Blacksmith", isBlacksmithOffering), Needs: nil, Cleanup: false},
		{Name: push, Run: r.pushStep(st, role), Needs: []string{find}, Cleanup: false},
		{Name: create, Run: r.createInstanceStep(st, role, ""), Needs: []string{find}, Cleanup: false},
		{Name: key, Run: r.serviceKeyStep(st), Needs: []string{create}, Cleanup: false},
		{Name: bind, Run: r.bindStep(st, role, "", false), Needs: []string{create, push}, Cleanup: false},
		{Name: stepName(suite, "unbind_app"), Run: r.unbindStep(st, role), Needs: []string{bind}, Cleanup: true},
		{Name: stepName(suite, "delete_service_key"), Run: r.deleteServiceKeyStep(st), Needs: []string{key}, Cleanup: true},
		{Name: stepName(suite, "delete_instance"), Run: r.deleteInstanceStep(st), Needs: []string{create}, Cleanup: true},
		{Name: stepName(suite, "delete_app"), Run: r.deleteStep(st, role), Needs: []string{push}, Cleanup: true},
	}
}

// findOfferingStep reads the marketplace and picks the offering and plan.
func (r *TestRunner) findOfferingStep(st *suiteState, offeringName, planName, what string,
	match func(marketplaceOffering) bool,
) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		offerings, err := r.loadMarketplace(ctx)
		if err != nil {
			return "", err
		}

		offering, plan, skip, err := selectOffering(offerings, offeringName, planName, what, match)
		if err != nil || skip != "" {
			return skip, err
		}

		err = r.ensurePlanAccess(ctx, offering, plan)
		if err != nil {
			return "", err
		}

		st.offering, st.plan = offering, plan

		log.Printf("selected %s/%s from broker %s (%s visibility)", offering.Name, plan.Name, offering.Broker, plan.Visibility)

		return "", nil
	}
}

// createInstanceStep creates the suite's service instance. The name is
// recorded before the create so cleanup can delete a half-created instance.
func (r *TestRunner) createInstanceStep(st *suiteState, role, params string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		name := r.appName(role + "-si")
		st.instance = ccServiceInstance{GUID: "", Name: name, LastOperation: ccLastOperation{Type: "", State: "", Description: ""}}

		instance, err := r.createServiceInstance(ctx, log, st.offering, st.plan, name, params)
		if err != nil {
			return "", err
		}

		st.instance = instance

		return "", nil
	}
}

// serviceKeyStep creates a service key and confirms it carries credentials.
func (r *TestRunner) serviceKeyStep(st *suiteState) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		st.keyName = r.appName("key")

		details, err := r.createServiceKey(ctx, log, st.instance, st.keyName)
		if err != nil {
			return "", err
		}

		keys := credentialKeys(details)
		if len(keys) == 0 {
			return "", fmt.Errorf("%w: %s", ErrTestNoCredentials, st.keyName)
		}

		log.Printf("service key %s carries credentials: %s", st.keyName, strings.Join(keys, ", "))

		return "", nil
	}
}

// bindStep binds the instance to the app, optionally requiring volume mounts.
func (r *TestRunner) bindStep(st *suiteState, role, params string, wantVolume bool) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		details, err := r.bindService(ctx, log, st.apps[role], st.instance, params)
		if err != nil {
			return "", err
		}

		if wantVolume && len(details.VolumeMounts) == 0 {
			return "", ErrTestNoVolumeMounts
		}

		if wantVolume {
			log.Printf("binding carries %d volume mount(s)", len(details.VolumeMounts))
		}

		return "", nil
	}
}

func (r *TestRunner) unbindStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		app := st.apps[role]
		if app == nil {
			return "", nil
		}

		err := r.unbindService(ctx, app, st.instance)
		if err != nil {
			return "", err
		}

		log.Printf("unbound %s from %s", st.instance.Name, app.Name)

		return "", nil
	}
}

func (r *TestRunner) deleteServiceKeyStep(st *suiteState) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		err := r.deleteServiceKey(ctx, st.instance, st.keyName)
		if err != nil {
			return "", err
		}

		log.Printf("deleted service key %s", st.keyName)

		return "", nil
	}
}

func (r *TestRunner) deleteInstanceStep(st *suiteState) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		if st.instance.Name == "" {
			return "", nil
		}

		return "", r.deleteServiceInstance(ctx, log, st.instance.Name)
	}
}

// volumeSteps exercises the nfs or smb volume service broker.
func (r *TestRunner) volumeSteps(suite TestSuite, st *suiteState) []testStep {
	kind := string(suite)
	role := kind

	find := stepName(suite, "find_broker")
	push := stepName(suite, "push_app")
	create := stepName(suite, "create_instance")
	bind := stepName(suite, "bind_app")

	return []testStep{
		{Name: find, Run: r.findVolumeBrokerStep(st, kind), Needs: nil, Cleanup: false},
		{Name: push, Run: r.pushStep(st, role), Needs: []string{find}, Cleanup: false},
		{Name: create, Run: r.createInstanceStep(st, role, r.volumeCreateParams(kind)), Needs: []string{find}, Cleanup: false},
		{Name: bind, Run: r.bindStep(st, role, r.volumeBindParams(kind), true), Needs: []string{create, push}, Cleanup: false},
		{Name: stepName(suite, "volume_mount_on_restart"), Run: r.restartStep(st, role), Needs: []string{bind}, Cleanup: false},
		{Name: stepName(suite, "unbind_app"), Run: r.unbindStep(st, role), Needs: []string{bind}, Cleanup: true},
		{Name: stepName(suite, "delete_instance"), Run: r.deleteInstanceStep(st), Needs: []string{create}, Cleanup: true},
		{Name: stepName(suite, "delete_app"), Run: r.deleteStep(st, role), Needs: []string{push}, Cleanup: true},
	}
}

// volumeShare returns the share configured for kind.
func (r *TestRunner) volumeShare(kind string) string {
	if kind == string(TestSuiteSMB) {
		return r.SMBShare
	}

	return r.NFSShare
}

// volumeCreateParams builds the create-service parameters for kind.
func (r *TestRunner) volumeCreateParams(kind string) string {
	return mustJSON(map[string]string{"share": r.volumeShare(kind)})
}

// volumeBindParams builds the bind-service parameters for kind.
func (r *TestRunner) volumeBindParams(kind string) string {
	if kind != string(TestSuiteSMB) || r.SMBUsername == "" {
		return ""
	}

	return mustJSON(map[string]string{"username": r.SMBUsername, "password": r.SMBPassword})
}

// mustJSON encodes a string map; string maps cannot fail to encode.
func mustJSON(v map[string]string) string {
	out, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}

	return string(out)
}

// findVolumeBrokerStep finds the volume broker and confirms a share was
// given, skipping with a reason otherwise.
func (r *TestRunner) findVolumeBrokerStep(st *suiteState, kind string) stepFunc {
	find := r.findOfferingStep(st, "", "", kind+" volume", volumeOfferingMatcher(kind))

	return func(ctx context.Context, log *stepLog) (string, error) {
		skip, err := find(ctx, log)
		if err != nil || skip != "" {
			return skip, err
		}

		if r.volumeShare(kind) == "" {
			return fmt.Sprintf("%s broker %s is present but --%s-share was not given", kind, st.offering.Broker, kind), nil
		}

		return "", nil
	}
}

// restartStep restarts the app so the container starts with the volume
// mounted, then confirms the route still answers.
func (r *TestRunner) restartStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		app := st.apps[role]

		err := r.restartApp(ctx, log, app)
		if err != nil {
			return "", err
		}

		return "", r.waitForHTTP(ctx, log, app.appURL("/"), app.Marker)
	}
}

// tcpSteps routes the static app through the TCP domain and connects to it.
func (r *TestRunner) tcpSteps(suite TestSuite, st *suiteState) []testStep {
	const role = "tcp"

	find := stepName(suite, "find_tcp_domain")
	push := stepName(suite, "push_app")
	mapRoute := stepName(suite, "map_tcp_route")

	return []testStep{
		{Name: find, Run: r.findTCPDomainStep(st), Needs: nil, Cleanup: false},
		{Name: push, Run: r.pushStep(st, role), Needs: []string{find}, Cleanup: false},
		{Name: mapRoute, Run: r.mapTCPRouteStep(st, role), Needs: []string{push}, Cleanup: false},
		{Name: stepName(suite, "connect_tcp_route"), Run: r.connectTCPRouteStep(st, role), Needs: []string{mapRoute}, Cleanup: false},
		{Name: stepName(suite, "delete_app"), Run: r.deleteStep(st, role), Needs: []string{push}, Cleanup: true},
	}
}

func (r *TestRunner) findTCPDomainStep(st *suiteState) stepFunc {
	return func(_ context.Context, log *stepLog) (string, error) {
		domain, ok := findTCPDomain(r.domains, r.TCPDomain)
		if !ok {
			if r.TCPDomain != "" {
				return "", fmt.Errorf("%w: %s", ErrTestTCPDomainNotFound, r.TCPDomain)
			}

			return "no TCP domain on the foundation", nil
		}

		st.tcpDomain = domain

		log.Printf("tcp domain %s", domain.Name)

		return "", nil
	}
}

// mapTCPRouteStep maps a random-port TCP route and records the port.
func (r *TestRunner) mapTCPRouteStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		app := st.apps[role]

		_, err := r.cf(ctx, "map-route", app.Name, st.tcpDomain.Name)
		if err != nil {
			return "", err
		}

		routes, err := r.appRoutes(ctx, app)
		if err != nil {
			return "", err
		}

		for _, route := range routes {
			if route.Port != 0 {
				st.tcpPort = route.Port

				log.Printf("mapped tcp route %s:%d", st.tcpDomain.Name, route.Port)

				return "", nil
			}
		}

		return "", fmt.Errorf("%w with a port after map-route on %s", ErrTestNoRoute, st.tcpDomain.Name)
	}
}

func (r *TestRunner) connectTCPRouteStep(st *suiteState, role string) stepFunc {
	return func(ctx context.Context, log *stepLog) (string, error) {
		return "", r.waitForTCP(ctx, log, st.tcpDomain.Name, st.tcpPort, st.apps[role].Marker)
	}
}

// acceptanceErrandSteps runs the CF smoke-tests errand through the director.
func (r *TestRunner) acceptanceErrandSteps(alias string) []testStep {
	deployment := r.boshDeployment()

	run := func(ctx context.Context, log *stepLog) (string, error) {
		out, err := runner.Run(ctx, "bosh", "-e", alias, "-d", deployment, "run-errand", testBoshErrand)

		log.Printf("bosh -e %s -d %s run-errand %s:\n%s", alias, deployment, testBoshErrand, lastLines(string(out), 40))

		if err != nil {
			return "", fmt.Errorf("errand %s on %s/%s failed: %w: %s", testBoshErrand, alias, deployment, err, lastLines(string(out), 8))
		}

		return "", nil
	}

	return []testStep{
		{Name: stepName(TestSuiteAcceptance, "bosh_smoke_tests_errand"), Run: run, Needs: nil, Cleanup: false},
	}
}
