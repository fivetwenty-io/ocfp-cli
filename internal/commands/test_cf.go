package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrTestCCResponse is returned when the Cloud Controller answers a cf curl
// with an errors array instead of the requested resource.
var ErrTestCCResponse = errors.New("cloud controller returned an error")

// ErrTestNoResource is returned when a cf curl lookup finds no resource.
var ErrTestNoResource = errors.New("no matching resource")

// cfDomain is a Cloud Foundry domain as the suites see it.
type cfDomain struct {
	GUID      string
	Name      string
	Internal  bool
	Protocols []string
	Shared    bool
}

// isTCP reports whether the domain routes TCP traffic.
func (d cfDomain) isTCP() bool {
	return slices.Contains(d.Protocols, "tcp")
}

// isHTTP reports whether the domain routes HTTP traffic.
func (d cfDomain) isHTTP() bool {
	return len(d.Protocols) == 0 || slices.Contains(d.Protocols, "http")
}

// ccPage is one page of a Cloud Controller v3 list response.
type ccPage struct {
	Pagination struct {
		Next *struct {
			Href string `json:"href"`
		} `json:"next"`
	} `json:"pagination"`
	Resources []json.RawMessage `json:"resources"`
}

// ccResource is the name and guid shared by every v3 resource.
type ccResource struct {
	GUID string `json:"guid"`
	Name string `json:"name"`
}

// ccLastOperation is the asynchronous operation state on service resources.
type ccLastOperation struct {
	Type        string `json:"type"`
	State       string `json:"state"`
	Description string `json:"description"`
}

// ccServiceInstance is the subset of a v3 service instance the suites read.
type ccServiceInstance struct {
	GUID          string          `json:"guid"`
	Name          string          `json:"name"`
	LastOperation ccLastOperation `json:"last_operation"`
}

// ccBinding is the subset of a v3 service credential binding the suites read.
type ccBinding struct {
	GUID          string          `json:"guid"`
	Name          string          `json:"name"`
	LastOperation ccLastOperation `json:"last_operation"`
}

// ccBindingDetails is the credential payload of a binding.
type ccBindingDetails struct {
	Credentials  map[string]json.RawMessage `json:"credentials"`
	VolumeMounts []json.RawMessage          `json:"volume_mounts"`
}

// ccRoute is the subset of a v3 route the suites read.
type ccRoute struct {
	GUID     string `json:"guid"`
	Host     string `json:"host"`
	URL      string `json:"url"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// ccDomain is the wire form of a v3 domain.
type ccDomain struct {
	GUID               string   `json:"guid"`
	Name               string   `json:"name"`
	Internal           bool     `json:"internal"`
	SupportedProtocols []string `json:"supported_protocols"`
	Relationships      struct {
		Organization struct {
			Data *ccResource `json:"data"`
		} `json:"organization"`
	} `json:"relationships"`
}

// ccErrors is the Cloud Controller error envelope.
type ccErrors struct {
	Errors []struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	} `json:"errors"`
}

// cf runs a cf CLI command and returns its combined output. Failures carry
// the tail of the output so the operator sees what the CLI said.
func (r *TestRunner) cf(ctx context.Context, args ...string) (string, error) {
	out, err := runner.Run(ctx, "cf", args...)

	text := string(out)
	if err != nil {
		return text, fmt.Errorf("cf %s: %w: %s", strings.Join(redactCFArgs(args), " "), err, lastLines(text, 8))
	}

	return text, nil
}

// redactCFArgs hides JSON parameter payloads that carry a password.
func redactCFArgs(args []string) []string {
	out := slices.Clone(args)

	for i := 1; i < len(out); i++ {
		if out[i-1] == "-c" && strings.Contains(out[i], "password") {
			out[i] = "<redacted>"
		}
	}

	return out
}

// cfCurl fetches a Cloud Controller path and decodes the JSON into v. A
// response that carries an errors array is returned as ErrTestCCResponse.
func (r *TestRunner) cfCurl(ctx context.Context, path string, v any) error {
	out, err := runner.Output(ctx, "cf", "curl", path)
	if err != nil {
		return fmt.Errorf("cf curl %s: %w", path, err)
	}

	return decodeCCResponse(path, out, v)
}

// decodeCCResponse decodes a Cloud Controller body into v, surfacing the
// error envelope when the API answered with one.
func decodeCCResponse(path string, body []byte, v any) error {
	var envelope ccErrors

	if json.Unmarshal(body, &envelope) == nil && len(envelope.Errors) > 0 {
		e := envelope.Errors[0]

		return fmt.Errorf("%w for %s: %s: %s", ErrTestCCResponse, path, e.Title, e.Detail)
	}

	err := json.Unmarshal(body, v)
	if err != nil {
		return fmt.Errorf("cf curl %s returned unexpected output: %w: %s", path, err, lastLines(string(body), 3))
	}

	return nil
}

// cfCurlAll fetches every page of a v3 list and returns the raw resources.
func (r *TestRunner) cfCurlAll(ctx context.Context, path string) ([]json.RawMessage, error) {
	var resources []json.RawMessage

	for path != "" {
		var page ccPage

		err := r.cfCurl(ctx, path, &page)
		if err != nil {
			return nil, err
		}

		resources = append(resources, page.Resources...)

		path = ""
		if page.Pagination.Next != nil {
			path = hrefToPath(page.Pagination.Next.Href)
		}
	}

	return resources, nil
}

// hrefToPath strips the scheme and host from a pagination href so it can be
// handed back to cf curl.
func hrefToPath(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}

	if u.RawQuery == "" {
		return u.Path
	}

	return u.Path + "?" + u.RawQuery
}

// ccQuery builds a v3 list path with the given filters and a large page size.
func ccQuery(path string, filters map[string]string) string {
	q := url.Values{}
	q.Set("per_page", "200")

	for k, v := range filters {
		q.Set(k, v)
	}

	return path + "?" + q.Encode()
}

// lookupGUID returns the guid of the first resource matching the filters.
func (r *TestRunner) lookupGUID(ctx context.Context, path string, filters map[string]string) (string, error) {
	resources, err := r.cfCurlAll(ctx, ccQuery(path, filters))
	if err != nil {
		return "", err
	}

	if len(resources) == 0 {
		return "", fmt.Errorf("%w at %s with %v", ErrTestNoResource, path, filters)
	}

	var res ccResource

	err = json.Unmarshal(resources[0], &res)
	if err != nil {
		return "", fmt.Errorf("decoding %s resource: %w", path, err)
	}

	return res.GUID, nil
}

// loadDomains lists every domain visible to the targeted org.
func (r *TestRunner) loadDomains(ctx context.Context) ([]cfDomain, error) {
	resources, err := r.cfCurlAll(ctx, ccQuery("/v3/domains", nil))
	if err != nil {
		return nil, err
	}

	return parseCFDomains(resources)
}

// parseCFDomains converts raw v3 domain resources into cfDomain values.
func parseCFDomains(resources []json.RawMessage) ([]cfDomain, error) {
	domains := make([]cfDomain, 0, len(resources))

	for _, raw := range resources {
		var d ccDomain

		err := json.Unmarshal(raw, &d)
		if err != nil {
			return nil, fmt.Errorf("decoding domain: %w", err)
		}

		domains = append(domains, cfDomain{
			GUID:      d.GUID,
			Name:      d.Name,
			Internal:  d.Internal,
			Protocols: d.SupportedProtocols,
			Shared:    d.Relationships.Organization.Data == nil,
		})
	}

	return domains, nil
}

// firstHTTPDomain returns the first shared, non-internal HTTP domain name.
func firstHTTPDomain(domains []cfDomain) string {
	for _, d := range domains {
		if d.Shared && !d.Internal && d.isHTTP() && !d.isTCP() {
			return d.Name
		}
	}

	return ""
}

// findInternalDomain returns the first internal domain name, preferring
// apps.internal when several exist.
func findInternalDomain(domains []cfDomain) string {
	name := ""

	for _, d := range domains {
		if !d.Internal {
			continue
		}

		if d.Name == "apps.internal" {
			return d.Name
		}

		if name == "" {
			name = d.Name
		}
	}

	return name
}

// findTCPDomain returns the TCP domain to route through. With an override
// the domain must exist and route TCP; otherwise the first TCP domain wins.
func findTCPDomain(domains []cfDomain, override string) (cfDomain, bool) {
	for _, d := range domains {
		if override != "" {
			if d.Name == override && d.isTCP() {
				return d, true
			}

			continue
		}

		if d.isTCP() {
			return d, true
		}
	}

	return cfDomain{}, false //nolint:exhaustruct // zero value signals not found
}

// parseCFAPIEndpoint extracts the endpoint from cf api's output, or "" when
// no endpoint is targeted.
func parseCFAPIEndpoint(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "API endpoint:")
		if ok {
			return strings.TrimRight(strings.TrimSpace(rest), "/")
		}
	}

	return ""
}

// cfConfigPath returns the cf CLI's config file, honouring CF_HOME.
func cfConfigPath() string {
	home := os.Getenv("CF_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	return filepath.Join(home, ".cf", "config.json")
}

// readCFSSLDisabled reports whether the cf CLI config at path was written
// with --skip-ssl-validation. Any read or parse problem reads as false.
func readCFSSLDisabled(path string) bool {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the cf CLI's own config file
	if err != nil {
		return false
	}

	var cfg struct {
		SSLDisabled bool `json:"SSLDisabled"`
	}

	err = json.Unmarshal(data, &cfg)
	if err != nil {
		return false
	}

	return cfg.SSLDisabled
}

// poll calls check every interval until it reports done, fails, or timeout
// elapses.
func (r *TestRunner) poll(ctx context.Context, timeout, interval time.Duration, what string,
	check func(ctx context.Context) (bool, error),
) error {
	deadline := time.Now().Add(timeout)

	for {
		done, err := check(ctx)
		if err != nil {
			return err
		}

		if done {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%w for %s after %s", ErrTestWaitTimeout, what, timeout)
		}

		err = r.sleep(ctx, interval)
		if err != nil {
			return fmt.Errorf("waiting for %s: %w", what, err)
		}
	}
}

// marketplacePlan is one plan of a marketplace offering.
type marketplacePlan struct {
	Name       string
	GUID       string
	Available  bool
	Visibility string
}

// marketplaceOffering is a service offering with its broker and plans.
type marketplaceOffering struct {
	Name   string
	GUID   string
	Broker string
	Tags   []string
	Plans  []marketplacePlan
}

// hasTag reports whether the offering carries tag (case-insensitive).
func (o marketplaceOffering) hasTag(tag string) bool {
	for _, t := range o.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}

	return false
}

// firstAvailablePlan returns the first available plan, preferring public
// visibility so no service-access change is needed.
func (o marketplaceOffering) firstAvailablePlan() *marketplacePlan {
	for i := range o.Plans {
		if o.Plans[i].Available && o.Plans[i].Visibility == "public" {
			return &o.Plans[i]
		}
	}

	for i := range o.Plans {
		if o.Plans[i].Available {
			return &o.Plans[i]
		}
	}

	return nil
}

// ccOffering is the wire form of a v3 service offering.
type ccOffering struct {
	GUID          string   `json:"guid"`
	Name          string   `json:"name"`
	Tags          []string `json:"tags"`
	Relationships struct {
		ServiceBroker struct {
			Data ccResource `json:"data"`
		} `json:"service_broker"`
	} `json:"relationships"`
}

// ccPlan is the wire form of a v3 service plan.
type ccPlan struct {
	GUID           string `json:"guid"`
	Name           string `json:"name"`
	Available      bool   `json:"available"`
	VisibilityType string `json:"visibility_type"`
	Relationships  struct {
		ServiceOffering struct {
			Data ccResource `json:"data"`
		} `json:"service_offering"`
	} `json:"relationships"`
}

// loadMarketplace reads brokers, offerings, and plans and joins them.
func (r *TestRunner) loadMarketplace(ctx context.Context) ([]marketplaceOffering, error) {
	brokers, err := r.cfCurlAll(ctx, ccQuery("/v3/service_brokers", nil))
	if err != nil {
		return nil, err
	}

	offerings, err := r.cfCurlAll(ctx, ccQuery("/v3/service_offerings", nil))
	if err != nil {
		return nil, err
	}

	plans, err := r.cfCurlAll(ctx, ccQuery("/v3/service_plans", nil))
	if err != nil {
		return nil, err
	}

	return parseMarketplace(brokers, offerings, plans)
}

// parseMarketplace joins raw broker, offering, and plan resources into
// offerings that know their broker name and plans. Offerings keep the order
// the Cloud Controller returned them in.
func parseMarketplace(brokers, offerings, plans []json.RawMessage) ([]marketplaceOffering, error) {
	brokerNames := make(map[string]string, len(brokers))

	for _, raw := range brokers {
		var b ccResource

		err := json.Unmarshal(raw, &b)
		if err != nil {
			return nil, fmt.Errorf("decoding service broker: %w", err)
		}

		brokerNames[b.GUID] = b.Name
	}

	result := make([]marketplaceOffering, 0, len(offerings))
	index := make(map[string]int, len(offerings))

	for _, raw := range offerings {
		var o ccOffering

		err := json.Unmarshal(raw, &o)
		if err != nil {
			return nil, fmt.Errorf("decoding service offering: %w", err)
		}

		index[o.GUID] = len(result)
		result = append(result, marketplaceOffering{
			Name:   o.Name,
			GUID:   o.GUID,
			Broker: brokerNames[o.Relationships.ServiceBroker.Data.GUID],
			Tags:   o.Tags,
			Plans:  nil,
		})
	}

	for _, raw := range plans {
		var p ccPlan

		err := json.Unmarshal(raw, &p)
		if err != nil {
			return nil, fmt.Errorf("decoding service plan: %w", err)
		}

		i, ok := index[p.Relationships.ServiceOffering.Data.GUID]
		if !ok {
			continue
		}

		result[i].Plans = append(result[i].Plans, marketplacePlan{
			Name:       p.Name,
			GUID:       p.GUID,
			Available:  p.Available,
			Visibility: p.VisibilityType,
		})
	}

	return result, nil
}

// isBlacksmithOffering reports whether the offering's broker is Blacksmith.
func isBlacksmithOffering(o marketplaceOffering) bool {
	return strings.Contains(strings.ToLower(o.Broker), "blacksmith")
}

// volumeOfferingMatcher matches the nfs or smb volume service offering by
// offering name or tag.
func volumeOfferingMatcher(kind string) func(marketplaceOffering) bool {
	return func(o marketplaceOffering) bool {
		return strings.EqualFold(o.Name, kind) || o.hasTag(kind)
	}
}

// selectOffering picks the offering and plan to test. An explicit offering
// or plan name that does not exist is an error; when nothing is named and
// no offering matches, the returned skip reason explains why.
func selectOffering(offerings []marketplaceOffering, offeringName, planName, what string,
	match func(marketplaceOffering) bool,
) (*marketplaceOffering, *marketplacePlan, string, error) {
	var offering *marketplaceOffering

	for i := range offerings {
		o := &offerings[i]

		if offeringName != "" {
			if o.Name == offeringName {
				offering = o

				break
			}

			continue
		}

		if match(*o) && o.firstAvailablePlan() != nil {
			offering = o

			break
		}
	}

	if offering == nil {
		if offeringName != "" {
			return nil, nil, "", fmt.Errorf("%w: %s", ErrTestOfferingNotFound, offeringName)
		}

		return nil, nil, "no " + what + " service offering with an available plan in the marketplace", nil
	}

	if planName == "" {
		plan := offering.firstAvailablePlan()
		if plan == nil {
			return nil, nil, "", fmt.Errorf("%w: %s has no available plan", ErrTestPlanNotFound, offering.Name)
		}

		return offering, plan, "", nil
	}

	for i := range offering.Plans {
		if offering.Plans[i].Name == planName {
			return offering, &offering.Plans[i], "", nil
		}
	}

	return nil, nil, "", fmt.Errorf("%w: %s/%s", ErrTestPlanNotFound, offering.Name, planName)
}

// ensurePlanAccess enables the plan for the test org when it is not public.
func (r *TestRunner) ensurePlanAccess(ctx context.Context, offering *marketplaceOffering, plan *marketplacePlan) error {
	if plan.Visibility == "public" {
		return nil
	}

	_, err := r.cf(ctx, "enable-service-access", offering.Name, "-b", offering.Broker, "-p", plan.Name, "-o", r.orgName)

	return err
}

// createServiceInstance creates an instance and waits for the asynchronous
// create to succeed. An instance that already exists is waited on instead.
func (r *TestRunner) createServiceInstance(ctx context.Context, log *stepLog, offering *marketplaceOffering,
	plan *marketplacePlan, name, params string,
) (ccServiceInstance, error) {
	args := []string{"create-service", offering.Name, plan.Name, name, "-b", offering.Broker}
	if params != "" {
		args = append(args, "-c", params)
	}

	out, err := r.cf(ctx, args...)
	if err != nil && !strings.Contains(out, "already exists") {
		return ccServiceInstance{}, err //nolint:exhaustruct // zero value on error
	}

	log.Printf("created service instance %s (%s/%s from %s)", name, offering.Name, plan.Name, offering.Broker)

	return r.waitForServiceInstance(ctx, log, name)
}

// serviceInstance looks up an instance by name in the test space.
func (r *TestRunner) serviceInstance(ctx context.Context, name string) (*ccServiceInstance, error) {
	filters := map[string]string{"names": name, "space_guids": r.spaceGUID}

	resources, err := r.cfCurlAll(ctx, ccQuery("/v3/service_instances", filters))
	if err != nil {
		return nil, err
	}

	if len(resources) == 0 {
		return nil, nil //nolint:nilnil // absence is the answer callers poll for
	}

	var instance ccServiceInstance

	err = json.Unmarshal(resources[0], &instance)
	if err != nil {
		return nil, fmt.Errorf("decoding service instance: %w", err)
	}

	return &instance, nil
}

// waitForServiceInstance polls until the instance's last operation
// succeeds, failing on a failed operation or the service timeout.
func (r *TestRunner) waitForServiceInstance(ctx context.Context, log *stepLog, name string) (ccServiceInstance, error) {
	var found ccServiceInstance

	err := r.poll(ctx, r.ServiceTimeout, testPollInterval, "service instance "+name, func(ctx context.Context) (bool, error) {
		instance, err := r.serviceInstance(ctx, name)
		if err != nil {
			return false, err
		}

		if instance == nil {
			return false, fmt.Errorf("%w: service instance %s", ErrTestNoResource, name)
		}

		found = *instance

		return operationSettled(instance.LastOperation, "service instance "+name)
	})
	if err == nil {
		log.Printf("service instance %s %s %s", name, found.LastOperation.Type, found.LastOperation.State)
	}

	return found, err
}

// operationSettled interprets a last_operation: done on succeeded, error on
// failed, and keep waiting otherwise.
func operationSettled(op ccLastOperation, what string) (bool, error) {
	switch op.State {
	case "succeeded":
		return true, nil
	case "failed":
		return false, fmt.Errorf("%s %s failed: %s", what, op.Type, op.Description) //nolint:err113 // broker-supplied description
	default:
		return false, nil
	}
}

// deleteServiceInstance deletes the instance and waits until it is gone.
func (r *TestRunner) deleteServiceInstance(ctx context.Context, log *stepLog, name string) error {
	_, err := r.cf(ctx, "delete-service", name, "-f")
	if err != nil {
		return err
	}

	err = r.poll(ctx, r.ServiceTimeout, testPollInterval, "deletion of service instance "+name, func(ctx context.Context) (bool, error) {
		instance, err := r.serviceInstance(ctx, name)
		if err != nil {
			return false, err
		}

		if instance == nil {
			return true, nil
		}

		if instance.LastOperation.State == "failed" {
			return false, fmt.Errorf("service instance %s delete failed: %s", name, instance.LastOperation.Description) //nolint:err113 // broker-supplied description
		}

		return false, nil
	})
	if err != nil {
		return err
	}

	log.Printf("service instance %s deleted", name)

	return nil
}

// findBinding returns the first credential binding matching the filters, or
// nil when there is none.
func (r *TestRunner) findBinding(ctx context.Context, filters map[string]string) (*ccBinding, error) {
	resources, err := r.cfCurlAll(ctx, ccQuery("/v3/service_credential_bindings", filters))
	if err != nil {
		return nil, err
	}

	if len(resources) == 0 {
		return nil, nil //nolint:nilnil // absence is the answer callers poll for
	}

	var binding ccBinding

	err = json.Unmarshal(resources[0], &binding)
	if err != nil {
		return nil, fmt.Errorf("decoding service credential binding: %w", err)
	}

	return &binding, nil
}

// waitForBinding polls until the binding exists and its last operation has
// succeeded.
func (r *TestRunner) waitForBinding(ctx context.Context, what string, filters map[string]string) (ccBinding, error) {
	var found ccBinding

	err := r.poll(ctx, r.ServiceTimeout, testPollInterval, what, func(ctx context.Context) (bool, error) {
		binding, err := r.findBinding(ctx, filters)
		if err != nil {
			return false, err
		}

		if binding == nil {
			return false, nil
		}

		found = *binding

		if binding.LastOperation.State == "" {
			return true, nil
		}

		return operationSettled(binding.LastOperation, what)
	})

	return found, err
}

// waitForBindingGone polls until no binding matches the filters.
func (r *TestRunner) waitForBindingGone(ctx context.Context, what string, filters map[string]string) error {
	return r.poll(ctx, r.ServiceTimeout, testPollInterval, "removal of "+what, func(ctx context.Context) (bool, error) {
		binding, err := r.findBinding(ctx, filters)
		if err != nil {
			return false, err
		}

		if binding == nil {
			return true, nil
		}

		if binding.LastOperation.State == "failed" {
			return false, fmt.Errorf("%s delete failed: %s", what, binding.LastOperation.Description) //nolint:err113 // broker-supplied description
		}

		return false, nil
	})
}

// bindingDetails fetches the credentials and volume mounts of a binding.
func (r *TestRunner) bindingDetails(ctx context.Context, guid string) (ccBindingDetails, error) {
	var details ccBindingDetails

	err := r.cfCurl(ctx, "/v3/service_credential_bindings/"+guid+"/details", &details)

	return details, err
}

// createServiceKey creates a service key and returns its credentials once
// the key is ready.
func (r *TestRunner) createServiceKey(ctx context.Context, log *stepLog, instance ccServiceInstance, keyName string) (ccBindingDetails, error) {
	out, err := r.cf(ctx, "create-service-key", instance.Name, keyName)
	if err != nil && !strings.Contains(out, "already exists") {
		return ccBindingDetails{}, err //nolint:exhaustruct // zero value on error
	}

	filters := map[string]string{"service_instance_guids": instance.GUID, "type": "key", "names": keyName}

	binding, err := r.waitForBinding(ctx, "service key "+keyName, filters)
	if err != nil {
		return ccBindingDetails{}, err //nolint:exhaustruct // zero value on error
	}

	log.Printf("service key %s ready (%s)", keyName, binding.GUID)

	return r.bindingDetails(ctx, binding.GUID)
}

// deleteServiceKey deletes a service key and waits until it is gone.
func (r *TestRunner) deleteServiceKey(ctx context.Context, instance ccServiceInstance, keyName string) error {
	_, err := r.cf(ctx, "delete-service-key", instance.Name, keyName, "-f")
	if err != nil {
		return err
	}

	filters := map[string]string{"service_instance_guids": instance.GUID, "type": "key", "names": keyName}

	return r.waitForBindingGone(ctx, "service key "+keyName, filters)
}

// bindService binds the instance to the app and waits for the binding to
// settle, returning its details.
func (r *TestRunner) bindService(ctx context.Context, log *stepLog, app *testApp, instance ccServiceInstance, params string) (ccBindingDetails, error) {
	args := []string{"bind-service", app.Name, instance.Name}
	if params != "" {
		args = append(args, "-c", params)
	}

	_, err := r.cf(ctx, args...)
	if err != nil {
		return ccBindingDetails{}, err //nolint:exhaustruct // zero value on error
	}

	filters := map[string]string{"service_instance_guids": instance.GUID, "app_guids": app.GUID, "type": "app"}

	binding, err := r.waitForBinding(ctx, "binding of "+instance.Name+" to "+app.Name, filters)
	if err != nil {
		return ccBindingDetails{}, err //nolint:exhaustruct // zero value on error
	}

	log.Printf("bound %s to %s (%s)", instance.Name, app.Name, binding.GUID)

	return r.bindingDetails(ctx, binding.GUID)
}

// unbindService removes the binding and waits until it is gone.
func (r *TestRunner) unbindService(ctx context.Context, app *testApp, instance ccServiceInstance) error {
	_, err := r.cf(ctx, "unbind-service", app.Name, instance.Name)
	if err != nil {
		return err
	}

	filters := map[string]string{"service_instance_guids": instance.GUID, "app_guids": app.GUID, "type": "app"}

	return r.waitForBindingGone(ctx, "binding of "+instance.Name+" to "+app.Name, filters)
}

// credentialKeys returns the sorted top-level credential names, for output
// that proves a key carries credentials without printing secrets.
func credentialKeys(details ccBindingDetails) []string {
	keys := make([]string, 0, len(details.Credentials))
	for k := range details.Credentials {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// countLogLines counts the application log lines in cf logs --recent output.
func countLogLines(out string) int {
	count := 0

	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "[APP/") || strings.Contains(line, "[RTR/") ||
			strings.Contains(line, "[CELL/") || strings.Contains(line, "[API/") ||
			strings.Contains(line, "[STG/") {
			count++
		}
	}

	return count
}

// boshDeployment returns the deployment the acceptance errand targets.
func (r *TestRunner) boshDeployment() string {
	if r.BoshDeployment != "" {
		return r.BoshDeployment
	}

	if env := os.Getenv("BOSH_DEPLOYMENT"); env != "" {
		return env
	}

	return testDefaultBoshDeployment
}

// directorAvailability returns the BOSH environment alias to use, or the
// reason none is usable. The alias comes from --bosh-env, then
// BOSH_ENVIRONMENT, and must answer bosh env.
func (r *TestRunner) directorAvailability(ctx context.Context) (string, string) {
	alias := r.BoshEnv
	if alias == "" {
		alias = os.Getenv("BOSH_ENVIRONMENT")
	}

	if alias == "" {
		return "", "no BOSH director was named (--bosh-env is empty and BOSH_ENVIRONMENT is unset)"
	}

	err := runner.LookPath("bosh")
	if err != nil {
		return "", "the bosh CLI is not on PATH"
	}

	out, err := runner.Run(ctx, "bosh", "-e", alias, "env")
	if err != nil {
		return "", fmt.Sprintf("bosh -e %s env failed: %s", alias, firstLine(lastLines(string(out), 1)))
	}

	return alias, ""
}

// portString formats a port for cf arguments.
func portString(port int) string {
	return strconv.Itoa(port)
}
