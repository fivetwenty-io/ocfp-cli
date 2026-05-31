//go:build integration

// Package integration_test runs the 16-step BOSH CPI lifecycle harness
// against a live PVE cluster. Each test step calls the CPI binary via
// cpirpc.Client (one process per call) and asserts out-of-band PVE state
// via verify.PVEVerifier between create/delete steps.
//
// Required environment variables:
//
//	OCFP_PVE_HOST        PVE API host, e.g. "pve.example.com" (port default 8006)
//	OCFP_PVE_NODE        PVE cluster node name, e.g. "pve"
//	OCFP_STEMCELL_PATH   path to BOSH stemcell tarball (.tgz)
//
// Auth (one of the following pairs):
//
//	OCFP_PVE_TOKEN       full PVEAPIToken credential string
//	OCFP_PVE_USER        PVE username (used with OCFP_PVE_PASSWORD)
//	OCFP_PVE_PASSWORD    PVE password (used with OCFP_PVE_USER)
//
// Optional:
//
//	OCFP_CPI_BIN         path to compiled cpi binary (default: ./bin/cpi)
//	OCFP_PVE_PORT        PVE API port (default: 8006)
//	OCFP_AGENT_ID        agent UUID for create_vm (default: lifecycle-<pid>)
//	OCFP_NETWORK_BRIDGE  PVE bridge (default: vmbr0)
//	OCFP_NETWORK_IP      test VM IP (default: 192.168.1.250)
//	OCFP_NETWORK_RANGE   CIDR (default: 192.168.1.0/24)
//	OCFP_NETWORK_GATEWAY gateway (default: 192.168.1.1)
//	OCFP_NETWORK_DNS     JSON array of DNS servers (default: ["8.8.8.8"])
//	OCFP_VM_CORES        core count (default: 1)
//	OCFP_VM_MEMORY_MIB   memory in MiB (default: 1024)
//	OCFP_DISK_SIZE_MIB   persistent disk size in MiB (default: 1024)
//	OCFP_VM_STORAGE      PVE storage pool for VMs (required in CPI config)
//	OCFP_DISK_STORAGE    PVE storage pool for disks (required in CPI config)
//	OCFP_STEMCELL_STORAGE PVE storage pool for stemcells (required in CPI config)
//	OCFP_ISO_STORAGE     PVE storage pool for ISOs (required in CPI config)
//	OCFP_VMID_RANGE_START first VMID in lifecycle range (default: 900)
package integration_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/pve/verify"
	"github.com/ocfp/ocfp-cli-go/tests/integration/cleanup"
	"github.com/ocfp/ocfp-cli-go/tests/integration/cpirpc"
)

// lifecycleEnv holds all environment-derived test parameters.
type lifecycleEnv struct {
	// PVE connectivity
	pveHost     string
	pvePort     int
	pveNode     string
	apiToken    string
	pveUser     string
	pvePassword string

	// CPI binary
	cpiBin string

	// CPI config map passed to cpirpc.Client
	cpiConfig map[string]any

	// Stemcell
	stemcellPath string

	// create_vm network parameters
	networkBridge  string
	networkIP      string
	networkRange   string
	networkGateway string
	networkDNS     []string

	// VM/disk sizing
	agentID     string
	vmCores     int
	vmMemoryMiB int
	diskSizeMiB int
}

// resolveLifecycleEnv reads and validates all required/optional env vars.
// Returns nil + skips the test (t.Skip) when required vars are absent.
func resolveLifecycleEnv(t *testing.T) *lifecycleEnv {
	t.Helper()

	host := os.Getenv("OCFP_PVE_HOST")
	if host == "" {
		t.Skip("OCFP_PVE_HOST not set — skipping integration lifecycle test")
	}

	node := os.Getenv("OCFP_PVE_NODE")
	if node == "" {
		t.Skip("OCFP_PVE_NODE not set — skipping integration lifecycle test")
	}

	stemcellPath := os.Getenv("OCFP_STEMCELL_PATH")
	if stemcellPath == "" {
		t.Skip("OCFP_STEMCELL_PATH not set — skipping integration lifecycle test")
	}

	apiToken := os.Getenv("OCFP_PVE_TOKEN")
	pveUser := os.Getenv("OCFP_PVE_USER")
	pvePassword := os.Getenv("OCFP_PVE_PASSWORD")
	if apiToken == "" && (pveUser == "" || pvePassword == "") {
		t.Skip("OCFP_PVE_TOKEN (or OCFP_PVE_USER + OCFP_PVE_PASSWORD) not set — skipping integration lifecycle test")
	}

	// Validate stemcell path is readable.
	if _, err := os.Stat(stemcellPath); err != nil {
		t.Fatalf("OCFP_STEMCELL_PATH %q: %v", stemcellPath, err)
	}

	// Port with default 8006.
	port := 8006
	if portStr := os.Getenv("OCFP_PVE_PORT"); portStr != "" {
		n, err := strconv.Atoi(portStr)
		if err != nil || n <= 0 || n > 65535 {
			t.Fatalf("OCFP_PVE_PORT %q is not a valid port number", portStr)
		}
		port = n
	}

	// CPI binary path with default.
	cpiBin := os.Getenv("OCFP_CPI_BIN")
	if cpiBin == "" {
		cpiBin = "./bin/cpi"
	}
	if _, err := os.Stat(cpiBin); err != nil {
		t.Fatalf("CPI binary %q not found — build it first with 'make bin/cpi': %v", cpiBin, err)
	}

	// Optional storage pools (may be empty; CPI config uses empty strings).
	vmStorage := envOr("OCFP_VM_STORAGE", "")
	diskStorage := envOr("OCFP_DISK_STORAGE", "")
	stemcellStorage := envOr("OCFP_STEMCELL_STORAGE", "")
	isoStorage := envOr("OCFP_ISO_STORAGE", "")
	networkBridge := envOr("OCFP_NETWORK_BRIDGE", "vmbr0")

	// VMID range start.
	vmidRangeStart := 900
	if v := os.Getenv("OCFP_VMID_RANGE_START"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("OCFP_VMID_RANGE_START %q is not a valid integer", v)
		}
		vmidRangeStart = n
	}

	// Build CPI config map. The CPI binary reads this from a temp file written
	// by cpirpc.Client.
	cpiConfig := map[string]any{
		"host":             host,
		"port":             port,
		"user":             pveUser,
		"node":             node,
		"vm_storage":       vmStorage,
		"disk_storage":     diskStorage,
		"stemcell_storage": stemcellStorage,
		"iso_storage":      isoStorage,
		"network_bridge":   networkBridge,
		"verify_ssl":       false,
		"vmid_range_start": vmidRangeStart,
	}
	if apiToken != "" {
		cpiConfig["api_token"] = apiToken
	} else {
		cpiConfig["password"] = pvePassword
	}

	// Network parameters for create_vm.
	networkDNS := []string{"8.8.8.8"}
	if raw := os.Getenv("OCFP_NETWORK_DNS"); raw != "" {
		var parsed []string
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("OCFP_NETWORK_DNS is not a valid JSON array: %v", err)
		}
		networkDNS = parsed
	}

	// VM/disk sizing.
	vmCores := intEnvOr("OCFP_VM_CORES", 1)
	vmMemoryMiB := intEnvOr("OCFP_VM_MEMORY_MIB", 1024)
	diskSizeMiB := intEnvOr("OCFP_DISK_SIZE_MIB", 1024)

	agentID := os.Getenv("OCFP_AGENT_ID")
	if agentID == "" {
		agentID = fmt.Sprintf("lifecycle-%d", os.Getpid())
	}

	return &lifecycleEnv{
		pveHost:        host,
		pvePort:        port,
		pveNode:        node,
		apiToken:       apiToken,
		pveUser:        pveUser,
		pvePassword:    pvePassword,
		cpiBin:         cpiBin,
		cpiConfig:      cpiConfig,
		stemcellPath:   stemcellPath,
		networkBridge:  networkBridge,
		networkIP:      envOr("OCFP_NETWORK_IP", "192.168.1.250"),
		networkRange:   envOr("OCFP_NETWORK_RANGE", "192.168.1.0/24"),
		networkGateway: envOr("OCFP_NETWORK_GATEWAY", "192.168.1.1"),
		networkDNS:     networkDNS,
		agentID:        agentID,
		vmCores:        vmCores,
		vmMemoryMiB:    vmMemoryMiB,
		diskSizeMiB:    diskSizeMiB,
	}
}

// envOr returns the env var value or defaultVal when absent/empty.
func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// intEnvOr returns an env var as int, or defaultVal when absent/empty/invalid.
func intEnvOr(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultVal
	}
	return n
}

// stemcellMeta holds name and version extracted from stemcell.MF.
type stemcellMeta struct {
	Name    string
	Version string
}

// extractStemcellImage extracts the inner `image` file from the outer stemcell
// tarball and returns its path plus a cleanup function. The CPI expects the
// inner image file (gzipped tar of root.img), not the outer .tgz. Mirrors the
// Python lifecycle's extract_stemcell_image.
//
// Also reads stemcell.MF to return name and version.
func extractStemcellImage(t *testing.T, stemcellPath string) (imagePath string, meta stemcellMeta, cleanup func()) {
	t.Helper()

	f, err := os.Open(stemcellPath)
	if err != nil {
		t.Fatalf("open stemcell %q: %v", stemcellPath, err)
	}
	defer f.Close() //nolint:errcheck

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("open gzip reader for stemcell %q: %v", stemcellPath, err)
	}
	defer gz.Close() //nolint:errcheck

	tr := tar.NewReader(gz)

	tmpDir := t.TempDir()
	imagePath = filepath.Join(tmpDir, "image")

	var imageFound, mfFound bool
	nameRE := regexp.MustCompile(`(?m)^name\s*:\s*['"]?([^'"]+?)['"]?\s*$`)
	versionRE := regexp.MustCompile(`(?m)^version\s*:\s*['"]?([^'"]+?)['"]?\s*$`)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read stemcell tar %q: %v", stemcellPath, err)
		}

		switch hdr.Name {
		case "image":
			outF, err := os.Create(imagePath)
			if err != nil {
				t.Fatalf("create image temp file: %v", err)
			}
			if _, err := io.Copy(outF, tr); err != nil {
				outF.Close() //nolint:errcheck
				t.Fatalf("extract image from stemcell: %v", err)
			}
			if err := outF.Close(); err != nil {
				t.Fatalf("close extracted image: %v", err)
			}
			imageFound = true

		case "stemcell.MF":
			raw, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read stemcell.MF: %v", err)
			}
			mfText := string(raw)
			if m := nameRE.FindStringSubmatch(mfText); m != nil {
				meta.Name = strings.TrimSpace(m[1])
			}
			if m := versionRE.FindStringSubmatch(mfText); m != nil {
				meta.Version = strings.TrimSpace(m[1])
			}
			mfFound = true
		}

		if imageFound && mfFound {
			break
		}
	}

	if !imageFound {
		t.Fatalf("stemcell tarball %q has no 'image' member", stemcellPath)
	}
	if meta.Name == "" || meta.Version == "" {
		t.Fatalf("stemcell.MF in %q missing name or version field (name=%q version=%q)",
			stemcellPath, meta.Name, meta.Version)
	}

	return imagePath, meta, func() {} // t.TempDir() handles cleanup
}

// buildVerifier constructs a PVEVerifier from lifecycleEnv.
func buildVerifier(e *lifecycleEnv) *verify.PVEVerifier {
	base := fmt.Sprintf("https://%s:%d/api2/json", e.pveHost, e.pvePort)
	return verify.NewVerifier(base, e.pveNode, e.apiToken, e.pveUser, e.pvePassword)
}

// cpiCall wraps cpirpc.Client.Call with step logging and fatal-on-error.
// Returns the Response.Result as an any for the caller to type-assert.
func cpiCall(t *testing.T, client *cpirpc.Client, ctx context.Context, stepLabel, method string, args ...any) any {
	t.Helper()

	t.Logf("[%s] -> %s(%v)", stepLabel, method, argsLogString(args))

	req := cpirpc.Request{
		Method:    method,
		Arguments: args,
		Context: map[string]any{
			"request_id": fmt.Sprintf("%s-%d", method, time.Now().UnixNano()),
		},
	}

	resp, err := client.Call(ctx, req)
	if err != nil {
		t.Fatalf("[%s] %s: RPC error: %v", stepLabel, method, err)
	}
	if resp.Error != nil {
		t.Fatalf("[%s] %s: CPI error [%s]: %s (ok_to_retry=%v)",
			stepLabel, method, resp.Error.Type, resp.Error.Message, resp.Error.OkToRetry)
	}

	t.Logf("[%s] <- %s result: %v", stepLabel, method, resultLogString(resp.Result))
	return resp.Result
}

// argsLogString formats call args for logging. Truncates long strings.
func argsLogString(args []any) string {
	if len(args) == 0 {
		return "()"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("%v", args)
	}
	s := string(b)
	const maxLen = 200
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}

// resultLogString formats a result for logging.
func resultLogString(result any) string {
	if result == nil {
		return "<nil>"
	}
	b, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("%v", result)
	}
	s := string(b)
	const maxLen = 200
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}

// assertVMExists fails the test if the verifier reports a state inconsistent
// with want.
func assertVMExists(t *testing.T, v *verify.PVEVerifier, vmCID string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := v.VMExists(ctx, vmCID)
	if err != nil {
		t.Fatalf("verify VMExists(%q): %v", vmCID, err)
	}
	if got != want {
		t.Fatalf("verify VMExists(%q): got %v, want %v", vmCID, got, want)
	}
}

// assertVolumeExists fails the test if the verifier reports a state
// inconsistent with want.
func assertVolumeExists(t *testing.T, v *verify.PVEVerifier, diskCID string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := v.VolumeExists(ctx, diskCID)
	if err != nil {
		t.Fatalf("verify VolumeExists(%q): %v", diskCID, err)
	}
	if got != want {
		t.Fatalf("verify VolumeExists(%q): got %v, want %v", diskCID, got, want)
	}
}

// resultString extracts a string from a CPI result any, fataling on type mismatch.
// BOSH CPI results that are single CIDs are returned as plain strings from the
// JSON-decoded result field.
func resultString(t *testing.T, step string, result any) string {
	t.Helper()
	switch v := result.(type) {
	case string:
		return v
	case []any:
		// create_vm returns [vm_cid, ...]; take the first element.
		if len(v) == 0 {
			t.Fatalf("[%s] expected non-empty string/array result, got empty array", step)
		}
		s, ok := v[0].(string)
		if !ok {
			t.Fatalf("[%s] expected string in result[0], got %T: %v", step, v[0], v[0])
		}
		return s
	case map[string]any:
		t.Fatalf("[%s] expected string result, got map: %v", step, v)
	case nil:
		t.Fatalf("[%s] expected string result, got nil", step)
	default:
		// JSON numbers decode as float64; handle numeric CIDs (VMID).
		if f, ok := result.(float64); ok {
			return strconv.FormatInt(int64(f), 10)
		}
		t.Fatalf("[%s] expected string result, got %T: %v", step, result, result)
	}
	return ""
}

// resultBool extracts a boolean from a CPI result, fataling on type mismatch.
func resultBool(t *testing.T, step string, result any) bool {
	t.Helper()
	b, ok := result.(bool)
	if !ok {
		t.Fatalf("[%s] expected bool result, got %T: %v", step, result, result)
	}
	return b
}

// resultStringSlice extracts a []string from a CPI result, fataling on type mismatch.
func resultStringSlice(t *testing.T, step string, result any) []string {
	t.Helper()
	raw, ok := result.([]any)
	if !ok {
		t.Fatalf("[%s] expected array result, got %T: %v", step, result, result)
	}
	out := make([]string, 0, len(raw))
	for i, elem := range raw {
		s, ok := elem.(string)
		if !ok {
			// Numeric disk CIDs (rare but possible): coerce.
			if f, ok := elem.(float64); ok {
				out = append(out, strconv.FormatInt(int64(f), 10))
				continue
			}
			t.Fatalf("[%s] result[%d] is %T, not string: %v", step, i, elem, elem)
		}
		out = append(out, s)
	}
	return out
}

// TestPVELifecycle_16Steps runs the full CPI lifecycle sequence against a live
// PVE cluster. Skipped when OCFP_PVE_HOST (or other required vars) is absent.
//
// Sub-tests mirror the 16-step docstring in scripts/lifecycle and map directly
// to BOSH director call order. Each sub-test calls t.Fatal on the first
// assertion failure; a t.Cleanup registered at test start attempts best-effort
// reverse-order resource deletion so the PVE cluster is not left with orphans.
func TestPVELifecycle_16Steps(t *testing.T) {
	e := resolveLifecycleEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client := cpirpc.New(e.cpiBin, e.cpiConfig)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Logf("cpirpc.Client.Close: %v", err)
		}
	})

	verifier := buildVerifier(e)

	// Mutable state threaded between steps. Tracks created resource CIDs for
	// cleanup. The t.Cleanup below reads these directly; closures capture the
	// pointer so updates after registration are visible at cleanup time.
	var (
		stemcellCID  string
		vmCID        string
		diskCID      string
		snapshotCID  string
		diskAttached bool
	)

	// tracker records each resource in creation order. t.Cleanup calls
	// tracker.Cleanup in reverse order (LIFO) using the CPI client. Runs on
	// test completion regardless of pass/fail; a background context ensures
	// cleanup proceeds even when the test context is cancelled.
	tracker := cleanup.New()

	// buildCleanCall returns a callback that invokes method with args via the
	// shared CPI client, logging but not fataling on error.
	buildCleanCall := func(method string, extraArgs ...any) func(context.Context, cleanup.Resource) error {
		return func(cleanCtx context.Context, r cleanup.Resource) error {
			args := make([]any, 0, 1+len(extraArgs))
			args = append(args, r.CID)
			args = append(args, extraArgs...)
			req := cpirpc.Request{
				Method:    method,
				Arguments: args,
				Context: map[string]any{
					"request_id": fmt.Sprintf("cleanup-%s-%d", method, time.Now().UnixNano()),
				},
			}
			resp, err := client.Call(cleanCtx, req)
			if err != nil {
				t.Logf("cleanup %s cid=%q: %v", method, r.CID, err)
				return err
			}
			if resp.Error != nil {
				t.Logf("cleanup %s cid=%q: CPI error [%s]: %s", method, r.CID, resp.Error.Type, resp.Error.Message)
				return resp.Error
			}
			return nil
		}
	}

	// Best-effort reverse-order cleanup via ResourceTracker.
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanCancel()

		// detach_disk before delete_disk when disk is still attached. Register
		// a synthetic detach resource immediately before the disk entry so it
		// runs first in reverse order (tracker is append-only; the detach
		// resource is tracked here to ensure it precedes disk cleanup).
		if diskAttached && vmCID != "" && diskCID != "" {
			// Inline detach: not a standard tracker kind, so call directly.
			req := cpirpc.Request{
				Method:    "detach_disk",
				Arguments: []any{vmCID, diskCID},
				Context: map[string]any{
					"request_id": fmt.Sprintf("cleanup-detach_disk-%d", time.Now().UnixNano()),
				},
			}
			resp, err := client.Call(cleanCtx, req)
			if err != nil {
				t.Logf("cleanup detach_disk vm=%q disk=%q: %v", vmCID, diskCID, err)
			} else if resp.Error != nil {
				t.Logf("cleanup detach_disk vm=%q disk=%q: CPI error [%s]: %s",
					vmCID, diskCID, resp.Error.Type, resp.Error.Message)
			}
		}

		callbacks := map[cleanup.ResourceKind]func(context.Context, cleanup.Resource) error{
			cleanup.KindSnapshot: buildCleanCall("delete_snapshot"),
			cleanup.KindDisk:     buildCleanCall("delete_disk"),
			cleanup.KindVM:       buildCleanCall("delete_vm"),
			cleanup.KindStemcell: buildCleanCall("delete_stemcell"),
		}

		if err := tracker.Cleanup(cleanCtx, callbacks); err != nil {
			t.Logf("resource tracker cleanup completed with errors: %v", err)
		}
	})

	// --- Step 1: info ---
	t.Run("step01_info", func(t *testing.T) {
		result := cpiCall(t, client, ctx, "step01", "info")
		if result == nil {
			t.Fatalf("info returned nil result")
		}
		info, ok := result.(map[string]any)
		if !ok {
			// Non-object result — accept any non-nil (some CPI versions return
			// a struct, some a string; the contract is simply non-nil).
			t.Logf("info result: %v", result)
			return
		}
		t.Logf("info: api_version=%v stemcell_formats=%v", info["api_version"], info["stemcell_formats"])
	})

	// --- Step 2: create_stemcell ---
	t.Run("step02_create_stemcell", func(t *testing.T) {
		imagePath, meta, _ := extractStemcellImage(t, e.stemcellPath)
		t.Logf("stemcell name=%q version=%q image=%q", meta.Name, meta.Version, imagePath)

		cloudProps := map[string]any{
			"name":    meta.Name,
			"version": meta.Version,
		}
		result := cpiCall(t, client, ctx, "step02", "create_stemcell", imagePath, cloudProps)
		stemcellCID = resultString(t, "step02_create_stemcell", result)
		if stemcellCID == "" {
			t.Fatalf("create_stemcell returned empty CID")
		}
		tracker.Track(cleanup.Resource{Kind: cleanup.KindStemcell, CID: stemcellCID})
		t.Logf("stemcell_cid=%q", stemcellCID)
	})

	// --- Step 3: create_vm ---
	t.Run("step03_create_vm", func(t *testing.T) {
		if stemcellCID == "" {
			t.Fatal("stemcellCID empty — step02 must have passed")
		}

		cloudProps := map[string]any{
			"cores":  e.vmCores,
			"memory": e.vmMemoryMiB,
		}
		networks := map[string]any{
			"default": map[string]any{
				"type":    "manual",
				"ip":      e.networkIP,
				"netmask": "255.255.255.0",
				"gateway": e.networkGateway,
				"dns":     e.networkDNS,
				"default": []string{"dns", "gateway"},
				"cloud_properties": map[string]any{
					"bridge": e.networkBridge,
				},
			},
		}

		result := cpiCall(t, client, ctx, "step03", "create_vm",
			e.agentID,
			stemcellCID,
			cloudProps,
			networks,
			[]any{},
			map[string]any{},
		)

		vmCID = resultString(t, "step03_create_vm", result)
		if vmCID == "" {
			t.Fatalf("create_vm returned empty CID")
		}
		tracker.Track(cleanup.Resource{Kind: cleanup.KindVM, CID: vmCID})
		t.Logf("vm_cid=%q", vmCID)

		// Out-of-band: VM must exist in PVE.
		assertVMExists(t, verifier, vmCID, true)
		t.Logf("verify: vm %q exists on PVE", vmCID)
	})

	// --- Step 4: has_vm (post-create, assert true) ---
	t.Run("step04_has_vm_true", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		result := cpiCall(t, client, ctx, "step04", "has_vm", vmCID)
		if !resultBool(t, "step04_has_vm_true", result) {
			t.Fatalf("has_vm(%q) returned false, expected true", vmCID)
		}
		t.Logf("has_vm=%v", true)
	})

	// --- Step 5: set_vm_metadata ---
	t.Run("step05_set_vm_metadata", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		metadata := map[string]any{
			"deployment": "lifecycle-test",
			"director":   "ocfp-lifecycle-harness",
		}
		cpiCall(t, client, ctx, "step05", "set_vm_metadata", vmCID, metadata)
		t.Logf("set_vm_metadata=ok")
	})

	// --- Step 6: create_disk ---
	t.Run("step06_create_disk", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		result := cpiCall(t, client, ctx, "step06", "create_disk",
			e.diskSizeMiB,
			map[string]any{},
			vmCID,
		)
		diskCID = resultString(t, "step06_create_disk", result)
		if diskCID == "" {
			t.Fatalf("create_disk returned empty CID")
		}
		tracker.Track(cleanup.Resource{Kind: cleanup.KindDisk, CID: diskCID})
		t.Logf("disk_cid=%q", diskCID)

		// Out-of-band: volume must exist in PVE storage.
		assertVolumeExists(t, verifier, diskCID, true)
		t.Logf("verify: volume %q exists on PVE", diskCID)
	})

	// --- Step 6a: has_disk (post-create, assert true) ---
	t.Run("step06a_has_disk_true", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step06a", "has_disk", diskCID)
		if !resultBool(t, "step06a_has_disk_true", result) {
			t.Fatalf("has_disk(%q) returned false post-create, expected true", diskCID)
		}
		t.Logf("has_disk=true")
	})

	// --- Step 7: attach_disk ---
	t.Run("step07_attach_disk", func(t *testing.T) {
		if vmCID == "" || diskCID == "" {
			t.Fatal("vmCID or diskCID empty — steps 03/06 must have passed")
		}
		cpiCall(t, client, ctx, "step07", "attach_disk", vmCID, diskCID)
		diskAttached = true
		t.Logf("attach_disk=ok")
	})

	// --- Step 7a: set_disk_metadata ---
	t.Run("step07a_set_disk_metadata", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		metadata := map[string]any{
			"deployment":  "lifecycle-test",
			"director":    "ocfp-lifecycle-harness",
			"instance_id": e.agentID,
		}
		cpiCall(t, client, ctx, "step07a", "set_disk_metadata", diskCID, metadata)
		t.Logf("set_disk_metadata=ok")
	})

	// --- Step 7b: resize_disk (doubles disk size) ---
	t.Run("step07b_resize_disk", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		newSize := e.diskSizeMiB * 2
		cpiCall(t, client, ctx, "step07b", "resize_disk", diskCID, newSize)
		t.Logf("resize_disk: %d MiB -> %d MiB ok", e.diskSizeMiB, newSize)
	})

	// --- Step 7c: update_disk ---
	t.Run("step07c_update_disk", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		cloudProps := map[string]any{
			"cache":    "writeback",
			"iothread": true,
		}
		cpiCall(t, client, ctx, "step07c", "update_disk", diskCID, cloudProps)
		t.Logf("update_disk=ok")
	})

	// --- Step 8: get_disks (post-attach assertion) ---
	t.Run("step08_get_disks", func(t *testing.T) {
		if vmCID == "" || diskCID == "" {
			t.Fatal("vmCID or diskCID empty — steps 03/06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step08", "get_disks", vmCID)
		disks := resultStringSlice(t, "step08_get_disks", result)
		t.Logf("get_disks result: %v", disks)
		found := false
		for _, d := range disks {
			if d == diskCID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("get_disks did not include disk_cid=%q; disks=%v", diskCID, disks)
		}
	})

	// --- Step 9: snapshot_disk ---
	t.Run("step09_snapshot_disk", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step09", "snapshot_disk",
			diskCID,
			map[string]any{"deployment": "lifecycle-test"},
		)
		snapshotCID = resultString(t, "step09_snapshot_disk", result)
		if snapshotCID == "" {
			t.Fatalf("snapshot_disk returned empty CID")
		}
		tracker.Track(cleanup.Resource{Kind: cleanup.KindSnapshot, CID: snapshotCID})
		t.Logf("snapshot_cid=%q", snapshotCID)
	})

	// --- Step 10: delete_snapshot (clean mode) ---
	t.Run("step10_delete_snapshot", func(t *testing.T) {
		if snapshotCID == "" {
			t.Fatal("snapshotCID empty — step09 must have passed")
		}
		cpiCall(t, client, ctx, "step10", "delete_snapshot", snapshotCID)
		snapshotCID = ""
		t.Logf("delete_snapshot=ok")
	})

	// --- Step 11: detach_disk ---
	t.Run("step11_detach_disk", func(t *testing.T) {
		if vmCID == "" || diskCID == "" {
			t.Fatal("vmCID or diskCID empty — steps 03/06 must have passed")
		}
		cpiCall(t, client, ctx, "step11", "detach_disk", vmCID, diskCID)
		diskAttached = false
		t.Logf("detach_disk=ok")
	})

	// --- Step 12: delete_disk ---
	t.Run("step12_delete_disk", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		cpiCall(t, client, ctx, "step12", "delete_disk", diskCID)
		// do not clear diskCID yet — has_disk assertion below still needs it
		t.Logf("delete_disk=ok")
	})

	// --- Step 12a: has_disk (post-delete, assert false) ---
	t.Run("step12a_has_disk_false", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step12a", "has_disk", diskCID)
		if resultBool(t, "step12a_has_disk_false", result) {
			t.Fatalf("has_disk(%q) returned true post-delete, expected false", diskCID)
		}
		t.Logf("has_disk=false (post-delete)")

		// Out-of-band: volume must be gone from PVE storage.
		assertVolumeExists(t, verifier, diskCID, false)
		t.Logf("verify: volume %q absent from PVE", diskCID)

		diskCID = ""
	})

	// --- Step 13: reboot_vm (soft) ---
	t.Run("step13_reboot_vm_soft", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		cpiCall(t, client, ctx, "step13", "reboot_vm", vmCID)
		t.Logf("reboot_vm(soft)=ok")
		// Verify VM still exists after soft reboot.
		result := cpiCall(t, client, ctx, "step13_post", "has_vm", vmCID)
		if !resultBool(t, "step13_reboot_vm_soft/has_vm", result) {
			t.Fatalf("has_vm returned false after soft reboot_vm — VM %q gone", vmCID)
		}
	})

	// --- Step 14: reboot_vm (hard) ---
	t.Run("step14_reboot_vm_hard", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}

		// Hard reboot requires a separate CPI config with reboot_mode=hard.
		// Write a one-off config file, use a second Client for this call.
		hardConfig := cloneMapWithKey(e.cpiConfig, "reboot_mode", "hard")
		hardClient := cpirpc.New(e.cpiBin, hardConfig)
		t.Cleanup(func() {
			if err := hardClient.Close(); err != nil {
				t.Logf("hardClient.Close: %v", err)
			}
		})

		req := cpirpc.Request{
			Method:    "reboot_vm",
			Arguments: []any{vmCID},
			Context: map[string]any{
				"request_id": fmt.Sprintf("reboot_vm-hard-%d", time.Now().UnixNano()),
			},
		}
		resp, err := hardClient.Call(ctx, req)
		if err != nil {
			t.Fatalf("[step14] reboot_vm(hard): RPC error: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("[step14] reboot_vm(hard): CPI error [%s]: %s", resp.Error.Type, resp.Error.Message)
		}
		t.Logf("reboot_vm(hard)=ok")

		// Verify VM still exists after hard reboot.
		result := cpiCall(t, client, ctx, "step14_post", "has_vm", vmCID)
		if !resultBool(t, "step14_reboot_vm_hard/has_vm", result) {
			t.Fatalf("has_vm returned false after hard reboot_vm — VM %q gone", vmCID)
		}
	})

	// --- Step 15: delete_vm ---
	t.Run("step15_delete_vm", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		deletedVMCID := vmCID
		cpiCall(t, client, ctx, "step15", "delete_vm", vmCID)
		vmCID = ""
		t.Logf("delete_vm=ok")

		// Out-of-band: VM must be gone from PVE.
		assertVMExists(t, verifier, deletedVMCID, false)
		t.Logf("verify: vm %q absent from PVE", deletedVMCID)
	})

	// --- Step 16: delete_stemcell ---
	t.Run("step16_delete_stemcell", func(t *testing.T) {
		if stemcellCID == "" {
			t.Fatal("stemcellCID empty — step02 must have passed")
		}
		deletedStemcellCID := stemcellCID
		cpiCall(t, client, ctx, "step16", "delete_stemcell", stemcellCID)
		stemcellCID = ""
		t.Logf("delete_stemcell=ok")

		// Out-of-band: stemcell volume must be gone from PVE stemcell storage.
		// The CID format is "<storage>:<volid>"; VolumeExists parses the prefix.
		// Only check when the CID contains a colon (the storage:volid format).
		if strings.Contains(deletedStemcellCID, ":") {
			assertVolumeExists(t, verifier, deletedStemcellCID, false)
			t.Logf("verify: stemcell volume %q absent from PVE", deletedStemcellCID)
		} else {
			t.Logf("stemcell_cid=%q has no storage prefix — skipping volume verify", deletedStemcellCID)
		}
	})
}

// cloneMapWithKey returns a shallow copy of m with key=value set.
func cloneMapWithKey(m map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out[key] = value
	return out
}

// networkTestMode represents the OCFP_NETWORK_TEST_MODE value.
type networkTestMode string

const (
	networkModeOff    networkTestMode = "off"
	networkModeSDN    networkTestMode = "sdn"
	networkModeBridge networkTestMode = "bridge"
)

// parseNetworkTestMode parses the OCFP_NETWORK_TEST_MODE environment variable.
// Valid values: "" (treated as "off"), "off", "sdn", "bridge".
// Returns an error for unrecognised values.
func parseNetworkTestMode(raw string) (networkTestMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "off":
		return networkModeOff, nil
	case "sdn":
		return networkModeSDN, nil
	case "bridge":
		return networkModeBridge, nil
	default:
		return networkModeOff, fmt.Errorf("OCFP_NETWORK_TEST_MODE %q is invalid — must be off, sdn, or bridge", raw)
	}
}

// sdnNetworkEnv holds SDN-specific parameters for network test mode.
type sdnNetworkEnv struct {
	zone     string
	zoneType string
	vnet     string
	sdnRange string
	gateway  string
	ip       string
}

// resolveSDNEnv reads and validates SDN-specific env vars.
// Returns a descriptive error listing all missing required vars.
func resolveSDNEnv(t *testing.T) sdnNetworkEnv {
	t.Helper()

	zone := os.Getenv("OCFP_SDN_ZONE")
	vnet := os.Getenv("OCFP_SDN_VNET")
	sdnRange := os.Getenv("OCFP_SDN_RANGE")
	gateway := os.Getenv("OCFP_SDN_GATEWAY")
	ip := os.Getenv("OCFP_SDN_IP")

	var missing []string
	if zone == "" {
		missing = append(missing, "OCFP_SDN_ZONE")
	}
	if vnet == "" {
		missing = append(missing, "OCFP_SDN_VNET")
	}
	if sdnRange == "" {
		missing = append(missing, "OCFP_SDN_RANGE")
	}
	if gateway == "" {
		missing = append(missing, "OCFP_SDN_GATEWAY")
	}
	if ip == "" {
		missing = append(missing, "OCFP_SDN_IP")
	}
	if len(missing) > 0 {
		t.Skipf("OCFP_NETWORK_TEST_MODE=sdn requires: %s", strings.Join(missing, ", "))
	}

	zoneType := envOr("OCFP_SDN_ZONE_TYPE", "simple")

	return sdnNetworkEnv{
		zone:     zone,
		zoneType: zoneType,
		vnet:     vnet,
		sdnRange: sdnRange,
		gateway:  gateway,
		ip:       ip,
	}
}

// resolveBridgeTestIface reads OCFP_BRIDGE_TEST_IFACE; skips the test when absent.
func resolveBridgeTestIface(t *testing.T) string {
	t.Helper()
	iface := os.Getenv("OCFP_BRIDGE_TEST_IFACE")
	if iface == "" {
		t.Skip("OCFP_NETWORK_TEST_MODE=bridge requires OCFP_BRIDGE_TEST_IFACE")
	}
	return iface
}

// buildNetworkSpecSDN constructs the create_network network_spec for sdn mode.
// Mirrors Python build_network_spec() for NETWORK_TEST_MODE=sdn.
func buildNetworkSpecSDN(sdn sdnNetworkEnv) map[string]any {
	return map[string]any{
		"type":    "manual",
		"range":   sdn.sdnRange,
		"gateway": sdn.gateway,
		"cloud_properties": map[string]any{
			"zone":      sdn.zone,
			"zone_type": sdn.zoneType,
			"vnet":      sdn.vnet,
		},
	}
}

// buildNetworkSpecBridge constructs the create_network network_spec for bridge mode.
// Mirrors Python build_network_spec() for NETWORK_TEST_MODE=bridge.
func buildNetworkSpecBridge(e *lifecycleEnv, bridgeIface string) map[string]any {
	return map[string]any{
		"type":    "manual",
		"range":   e.networkRange,
		"gateway": e.networkGateway,
		"cloud_properties": map[string]any{
			"bridge": bridgeIface,
		},
	}
}

// networkCreateResult parses the create_network CPI result.
// Returns (networkCID, cloudProps). Mirrors Python: net_res[0] and net_res[2].
func networkCreateResult(t *testing.T, step string, result any) (string, map[string]any) {
	t.Helper()
	raw, ok := result.([]any)
	if !ok {
		t.Fatalf("[%s] create_network: expected array result, got %T: %v", step, result, result)
	}
	if len(raw) == 0 {
		t.Fatalf("[%s] create_network: empty result array", step)
	}
	networkCID, ok := raw[0].(string)
	if !ok {
		t.Fatalf("[%s] create_network: result[0] is %T, expected string", step, raw[0])
	}
	if networkCID == "" {
		t.Fatalf("[%s] create_network: returned empty network CID", step)
	}
	var cloudProps map[string]any
	if len(raw) > 2 {
		if cp, ok := raw[2].(map[string]any); ok {
			cloudProps = cp
		}
	}
	return networkCID, cloudProps
}

// assertVNetExists fails the test when the verifier's VNetExists result
// does not match want.
func assertVNetExists(t *testing.T, v *verify.PVEVerifier, vnetID string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := v.VNetExists(ctx, vnetID)
	if err != nil {
		t.Fatalf("verify VNetExists(%q): %v", vnetID, err)
	}
	if got != want {
		t.Fatalf("verify VNetExists(%q): got %v, want %v", vnetID, got, want)
	}
}

// assertZoneExists fails the test when the verifier's ZoneExists result
// does not match want.
func assertZoneExists(t *testing.T, v *verify.PVEVerifier, zone string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := v.ZoneExists(ctx, zone)
	if err != nil {
		t.Fatalf("verify ZoneExists(%q): %v", zone, err)
	}
	if got != want {
		t.Fatalf("verify ZoneExists(%q): got %v, want %v", zone, got, want)
	}
}

// assertSubnetPresent fails the test when the verifier's SubnetPresent result
// does not match want.
func assertSubnetPresent(t *testing.T, v *verify.PVEVerifier, vnetID, cidr string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := v.SubnetPresent(ctx, vnetID, cidr)
	if err != nil {
		t.Fatalf("verify SubnetPresent(%q, %q): %v", vnetID, cidr, err)
	}
	if got != want {
		t.Fatalf("verify SubnetPresent(%q, %q): got %v, want %v", vnetID, cidr, got, want)
	}
}

// assertBridgeExists fails the test when the verifier's BridgeExists result
// does not match want.
func assertBridgeExists(t *testing.T, v *verify.PVEVerifier, bridge string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := v.BridgeExists(ctx, bridge)
	if err != nil {
		t.Fatalf("verify BridgeExists(%q): %v", bridge, err)
	}
	if got != want {
		t.Fatalf("verify BridgeExists(%q): got %v, want %v", bridge, got, want)
	}
}
