//go:build integration

// Package integration_test — network test mode extension.
//
// Adds tests T72, T73, T74 for OCFP_NETWORK_TEST_MODE handling. Each mode
// runs the full 16-step CPI lifecycle with create_network injected as step 2a
// (after create_stemcell, before create_vm) and delete_network as step 16a
// (after delete_stemcell).
//
// T72 TestPVELifecycle_NetworkModeSDN: gated on OCFP_NETWORK_TEST_MODE=sdn and
// OCFP_PVE_HOST. Requires OCFP_SDN_ZONE, OCFP_SDN_VNET, OCFP_SDN_RANGE,
// OCFP_SDN_GATEWAY, OCFP_SDN_IP.
//
// T73 TestPVELifecycle_NetworkModeBridge: gated on OCFP_NETWORK_TEST_MODE=bridge
// and OCFP_PVE_HOST. Requires OCFP_BRIDGE_TEST_IFACE.
//
// T74 TestPVELifecycle_NetworkModeOff_Default: asserts parseNetworkTestMode("")
// returns off; does not require a live PVE host.
package integration_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/tests/integration/cpirpc"
)

// TestPVELifecycle_NetworkModeOff_Default verifies that when
// OCFP_NETWORK_TEST_MODE is unset, parseNetworkTestMode returns networkModeOff
// and no create_network step is added. No live PVE host required.
func TestPVELifecycle_NetworkModeOff_Default(t *testing.T) {
	// Ensure env var is unset for this test.
	prev := os.Getenv("OCFP_NETWORK_TEST_MODE")
	os.Unsetenv("OCFP_NETWORK_TEST_MODE")
	t.Cleanup(func() {
		if prev != "" {
			os.Setenv("OCFP_NETWORK_TEST_MODE", prev)
		}
	})

	mode, err := parseNetworkTestMode("")
	if err != nil {
		t.Fatalf("parseNetworkTestMode(%q): unexpected error: %v", "", err)
	}
	if mode != networkModeOff {
		t.Fatalf("parseNetworkTestMode(%q) = %q, want %q", "", mode, networkModeOff)
	}

	// Also verify that a blank env var round-trips correctly.
	mode2, err := parseNetworkTestMode(os.Getenv("OCFP_NETWORK_TEST_MODE"))
	if err != nil {
		t.Fatalf("parseNetworkTestMode(getenv) unexpected error: %v", err)
	}
	if mode2 != networkModeOff {
		t.Fatalf("parseNetworkTestMode(getenv) = %q, want %q", mode2, networkModeOff)
	}

	// Verify invalid values are rejected.
	_, err = parseNetworkTestMode("invalid-mode")
	if err == nil {
		t.Fatal("parseNetworkTestMode(\"invalid-mode\"): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("parseNetworkTestMode error message should contain 'invalid', got: %v", err)
	}
}

// TestPVELifecycle_NetworkModeSDN runs the full 16-step lifecycle with
// NETWORK_TEST_MODE=sdn. Step 2a creates an SDN vnet; the VM is provisioned
// on that vnet. Step 16a deletes the vnet. Out-of-band assertions via
// PVEVerifier confirm SDN resources appear and disappear.
//
// Skipped when OCFP_NETWORK_TEST_MODE != "sdn" or OCFP_PVE_HOST is absent.
func TestPVELifecycle_NetworkModeSDN(t *testing.T) {
	if got := strings.ToLower(strings.TrimSpace(os.Getenv("OCFP_NETWORK_TEST_MODE"))); got != "sdn" {
		t.Skipf("OCFP_NETWORK_TEST_MODE=%q — set to \"sdn\" to run this test", got)
	}

	e := resolveLifecycleEnv(t)
	sdn := resolveSDNEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client := cpirpc.New(e.cpiBin, e.cpiConfig)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Logf("cpirpc.Client.Close: %v", err)
		}
	})

	verifier := buildVerifier(e)

	var (
		stemcellCID  string
		networkCID   string
		vmCID        string
		diskCID      string
		snapshotCID  string
		diskAttached bool
	)

	// Best-effort cleanup mirrors scripts/lifecycle cleanup().
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanCancel()
		networkCleanCall := func(method string, args ...any) {
			req := cpirpc.Request{
				Method:    method,
				Arguments: args,
				Context:   map[string]any{"request_id": fmt.Sprintf("cleanup-%s-%d", method, time.Now().UnixNano())},
			}
			resp, err := client.Call(cleanCtx, req)
			if err != nil {
				t.Logf("cleanup %s: %v", method, err)
				return
			}
			if resp.Error != nil {
				t.Logf("cleanup %s: CPI error [%s]: %s", method, resp.Error.Type, resp.Error.Message)
			}
		}
		if snapshotCID != "" {
			networkCleanCall("delete_snapshot", snapshotCID)
		}
		if diskAttached && vmCID != "" && diskCID != "" {
			networkCleanCall("detach_disk", vmCID, diskCID)
		}
		if diskCID != "" {
			networkCleanCall("delete_disk", diskCID)
		}
		if vmCID != "" {
			networkCleanCall("delete_vm", vmCID)
		}
		if stemcellCID != "" {
			networkCleanCall("delete_stemcell", stemcellCID)
		}
		if networkCID != "" {
			networkCleanCall("delete_network", networkCID)
		}
	})

	// --- Step 1: info ---
	t.Run("step01_info", func(t *testing.T) {
		result := cpiCall(t, client, ctx, "step01", "info")
		if result == nil {
			t.Fatalf("info returned nil result")
		}
		if info, ok := result.(map[string]any); ok {
			t.Logf("info: api_version=%v stemcell_formats=%v", info["api_version"], info["stemcell_formats"])
		} else {
			t.Logf("info result: %v", result)
		}
	})

	// --- Step 2: create_stemcell ---
	t.Run("step02_create_stemcell", func(t *testing.T) {
		imagePath, meta, _ := extractStemcellImage(t, e.stemcellPath)
		t.Logf("stemcell name=%q version=%q image=%q", meta.Name, meta.Version, imagePath)
		cloudProps := map[string]any{"name": meta.Name, "version": meta.Version}
		result := cpiCall(t, client, ctx, "step02", "create_stemcell", imagePath, cloudProps)
		stemcellCID = resultString(t, "step02_create_stemcell", result)
		if stemcellCID == "" {
			t.Fatalf("create_stemcell returned empty CID")
		}
		t.Logf("stemcell_cid=%q", stemcellCID)
	})

	// --- Step 2a: create_network (SDN mode) ---
	// Effective network params for subsequent create_vm.
	effBridge := e.networkBridge
	effIP := e.networkIP
	effGateway := e.networkGateway

	t.Run("step02a_create_network_sdn", func(t *testing.T) {
		if stemcellCID == "" {
			t.Fatal("stemcellCID empty — step02 must have passed")
		}

		networkSpec := buildNetworkSpecSDN(sdn)
		result := cpiCall(t, client, ctx, "step02a", "create_network", networkSpec)

		var cloudProps map[string]any
		networkCID, cloudProps = networkCreateResult(t, "step02a_create_network_sdn", result)
		t.Logf("network_cid=%q cloud_props=%v", networkCID, cloudProps)

		// Out-of-band: vnet and subnet must exist.
		assertVNetExists(t, verifier, networkCID, true)
		t.Logf("verify: sdn vnet %q exists on PVE", networkCID)

		assertSubnetPresent(t, verifier, networkCID, sdn.sdnRange, true)
		t.Logf("verify: sdn subnet %q present on vnet %q", sdn.sdnRange, networkCID)

		// Derive effective bridge for create_vm from cloud_props (non-fatal fallback).
		if cp := cloudProps; cp != nil {
			if b, ok := cp["bridge"].(string); ok && b != "" {
				effBridge = b
			} else {
				// SDN CPI may return the vnet name as the realized bridge.
				effBridge = networkCID
			}
		} else {
			effBridge = networkCID
		}
		effIP = sdn.ip
		effGateway = sdn.gateway
		t.Logf("effective create_vm params: bridge=%q ip=%q gateway=%q", effBridge, effIP, effGateway)
	})

	// --- Step 3: create_vm ---
	t.Run("step03_create_vm", func(t *testing.T) {
		if stemcellCID == "" {
			t.Fatal("stemcellCID empty — step02 must have passed")
		}
		if networkCID == "" {
			t.Fatal("networkCID empty — step02a must have passed")
		}
		cloudProps := map[string]any{
			"cores":  e.vmCores,
			"memory": e.vmMemoryMiB,
		}
		networks := map[string]any{
			"default": map[string]any{
				"type":    "manual",
				"ip":      effIP,
				"netmask": "255.255.255.0",
				"gateway": effGateway,
				"dns":     e.networkDNS,
				"default": []string{"dns", "gateway"},
				"cloud_properties": map[string]any{
					"bridge": effBridge,
				},
			},
		}
		result := cpiCall(t, client, ctx, "step03", "create_vm",
			e.agentID, stemcellCID, cloudProps, networks, []any{}, map[string]any{})
		vmCID = resultString(t, "step03_create_vm", result)
		if vmCID == "" {
			t.Fatalf("create_vm returned empty CID")
		}
		t.Logf("vm_cid=%q", vmCID)
		assertVMExists(t, verifier, vmCID, true)
		t.Logf("verify: vm %q exists on PVE", vmCID)
	})

	// --- Step 4: has_vm ---
	t.Run("step04_has_vm_true", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		result := cpiCall(t, client, ctx, "step04", "has_vm", vmCID)
		if !resultBool(t, "step04_has_vm_true", result) {
			t.Fatalf("has_vm(%q) returned false, expected true", vmCID)
		}
	})

	// --- Step 5: set_vm_metadata ---
	t.Run("step05_set_vm_metadata", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		cpiCall(t, client, ctx, "step05", "set_vm_metadata", vmCID, map[string]any{
			"deployment": "lifecycle-network-sdn-test",
			"director":   "ocfp-lifecycle-harness",
		})
		t.Logf("set_vm_metadata=ok")
	})

	// --- Step 6: create_disk ---
	t.Run("step06_create_disk", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		result := cpiCall(t, client, ctx, "step06", "create_disk",
			e.diskSizeMiB, map[string]any{}, vmCID)
		diskCID = resultString(t, "step06_create_disk", result)
		if diskCID == "" {
			t.Fatalf("create_disk returned empty CID")
		}
		t.Logf("disk_cid=%q", diskCID)
		assertVolumeExists(t, verifier, diskCID, true)
		t.Logf("verify: volume %q exists on PVE", diskCID)
	})

	// --- Step 6a: has_disk ---
	t.Run("step06a_has_disk_true", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step06a", "has_disk", diskCID)
		if !resultBool(t, "step06a_has_disk_true", result) {
			t.Fatalf("has_disk(%q) returned false post-create, expected true", diskCID)
		}
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

	// --- Step 8: get_disks ---
	t.Run("step08_get_disks", func(t *testing.T) {
		if vmCID == "" || diskCID == "" {
			t.Fatal("vmCID or diskCID empty — steps 03/06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step08", "get_disks", vmCID)
		disks := resultStringSlice(t, "step08_get_disks", result)
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
			diskCID, map[string]any{"deployment": "lifecycle-network-sdn-test"})
		snapshotCID = resultString(t, "step09_snapshot_disk", result)
		if snapshotCID == "" {
			t.Fatalf("snapshot_disk returned empty CID")
		}
		t.Logf("snapshot_cid=%q", snapshotCID)
	})

	// --- Step 10: delete_snapshot ---
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
		t.Logf("delete_disk=ok")
	})

	// --- Step 12a: has_disk (post-delete) ---
	t.Run("step12a_has_disk_false", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step12a", "has_disk", diskCID)
		if resultBool(t, "step12a_has_disk_false", result) {
			t.Fatalf("has_disk(%q) returned true post-delete, expected false", diskCID)
		}
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
		result := cpiCall(t, client, ctx, "step13_post", "has_vm", vmCID)
		if !resultBool(t, "step13_reboot_vm_soft/has_vm", result) {
			t.Fatalf("has_vm returned false after soft reboot_vm — VM %q gone", vmCID)
		}
		t.Logf("reboot_vm(soft)=ok")
	})

	// --- Step 14: reboot_vm (hard) ---
	t.Run("step14_reboot_vm_hard", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
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
			Context:   map[string]any{"request_id": fmt.Sprintf("reboot_vm-hard-%d", time.Now().UnixNano())},
		}
		resp, err := hardClient.Call(ctx, req)
		if err != nil {
			t.Fatalf("[step14] reboot_vm(hard): RPC error: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("[step14] reboot_vm(hard): CPI error [%s]: %s", resp.Error.Type, resp.Error.Message)
		}
		result := cpiCall(t, client, ctx, "step14_post", "has_vm", vmCID)
		if !resultBool(t, "step14_reboot_vm_hard/has_vm", result) {
			t.Fatalf("has_vm returned false after hard reboot_vm — VM %q gone", vmCID)
		}
		t.Logf("reboot_vm(hard)=ok")
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
		if strings.Contains(deletedStemcellCID, ":") {
			assertVolumeExists(t, verifier, deletedStemcellCID, false)
			t.Logf("verify: stemcell volume %q absent from PVE", deletedStemcellCID)
		}
	})

	// --- Step 16a: delete_network (SDN mode) ---
	t.Run("step16a_delete_network_sdn", func(t *testing.T) {
		if networkCID == "" {
			t.Fatal("networkCID empty — step02a must have passed")
		}
		deletedNetCID := networkCID
		cpiCall(t, client, ctx, "step16a", "delete_network", networkCID)
		networkCID = ""
		t.Logf("delete_network=ok")

		// Out-of-band: vnet must be gone.
		assertVNetExists(t, verifier, deletedNetCID, false)
		t.Logf("verify: sdn vnet %q gone from PVE", deletedNetCID)
	})
}

// TestPVELifecycle_NetworkModeBridge runs the full 16-step lifecycle with
// NETWORK_TEST_MODE=bridge. Step 2a creates a bridge interface; the VM is
// provisioned on that bridge. Step 16a deletes the bridge. Out-of-band
// assertions via PVEVerifier confirm bridge appears and disappears.
//
// Skipped when OCFP_NETWORK_TEST_MODE != "bridge" or OCFP_PVE_HOST is absent.
func TestPVELifecycle_NetworkModeBridge(t *testing.T) {
	if got := strings.ToLower(strings.TrimSpace(os.Getenv("OCFP_NETWORK_TEST_MODE"))); got != "bridge" {
		t.Skipf("OCFP_NETWORK_TEST_MODE=%q — set to \"bridge\" to run this test", got)
	}

	e := resolveLifecycleEnv(t)
	bridgeIface := resolveBridgeTestIface(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client := cpirpc.New(e.cpiBin, e.cpiConfig)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Logf("cpirpc.Client.Close: %v", err)
		}
	})

	verifier := buildVerifier(e)

	var (
		stemcellCID  string
		networkCID   string
		vmCID        string
		diskCID      string
		snapshotCID  string
		diskAttached bool
	)

	// Best-effort cleanup.
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanCancel()
		bridgeCleanCall := func(method string, args ...any) {
			req := cpirpc.Request{
				Method:    method,
				Arguments: args,
				Context:   map[string]any{"request_id": fmt.Sprintf("cleanup-%s-%d", method, time.Now().UnixNano())},
			}
			resp, err := client.Call(cleanCtx, req)
			if err != nil {
				t.Logf("cleanup %s: %v", method, err)
				return
			}
			if resp.Error != nil {
				t.Logf("cleanup %s: CPI error [%s]: %s", method, resp.Error.Type, resp.Error.Message)
			}
		}
		if snapshotCID != "" {
			bridgeCleanCall("delete_snapshot", snapshotCID)
		}
		if diskAttached && vmCID != "" && diskCID != "" {
			bridgeCleanCall("detach_disk", vmCID, diskCID)
		}
		if diskCID != "" {
			bridgeCleanCall("delete_disk", diskCID)
		}
		if vmCID != "" {
			bridgeCleanCall("delete_vm", vmCID)
		}
		if stemcellCID != "" {
			bridgeCleanCall("delete_stemcell", stemcellCID)
		}
		if networkCID != "" {
			bridgeCleanCall("delete_network", networkCID)
		}
	})

	// --- Step 1: info ---
	t.Run("step01_info", func(t *testing.T) {
		result := cpiCall(t, client, ctx, "step01", "info")
		if result == nil {
			t.Fatalf("info returned nil result")
		}
		if info, ok := result.(map[string]any); ok {
			t.Logf("info: api_version=%v stemcell_formats=%v", info["api_version"], info["stemcell_formats"])
		} else {
			t.Logf("info result: %v", result)
		}
	})

	// --- Step 2: create_stemcell ---
	t.Run("step02_create_stemcell", func(t *testing.T) {
		imagePath, meta, _ := extractStemcellImage(t, e.stemcellPath)
		t.Logf("stemcell name=%q version=%q image=%q", meta.Name, meta.Version, imagePath)
		cloudProps := map[string]any{"name": meta.Name, "version": meta.Version}
		result := cpiCall(t, client, ctx, "step02", "create_stemcell", imagePath, cloudProps)
		stemcellCID = resultString(t, "step02_create_stemcell", result)
		if stemcellCID == "" {
			t.Fatalf("create_stemcell returned empty CID")
		}
		t.Logf("stemcell_cid=%q", stemcellCID)
	})

	// --- Step 2a: create_network (bridge mode) ---
	// Effective bridge for subsequent create_vm.
	effBridge := e.networkBridge

	t.Run("step02a_create_network_bridge", func(t *testing.T) {
		if stemcellCID == "" {
			t.Fatal("stemcellCID empty — step02 must have passed")
		}

		networkSpec := buildNetworkSpecBridge(e, bridgeIface)
		result := cpiCall(t, client, ctx, "step02a", "create_network", networkSpec)

		var cloudProps map[string]any
		networkCID, cloudProps = networkCreateResult(t, "step02a_create_network_bridge", result)
		t.Logf("network_cid=%q cloud_props=%v", networkCID, cloudProps)

		// Out-of-band: bridge must exist.
		assertBridgeExists(t, verifier, networkCID, true)
		t.Logf("verify: bridge %q exists on PVE node %q", networkCID, e.pveNode)

		// Derive effective bridge; cloud_props may carry the iface name.
		if cp := cloudProps; cp != nil {
			if b, ok := cp["bridge"].(string); ok && b != "" {
				effBridge = b
			} else {
				effBridge = networkCID
			}
		} else {
			effBridge = networkCID
		}
		t.Logf("effective create_vm bridge=%q", effBridge)
	})

	// --- Step 3: create_vm ---
	t.Run("step03_create_vm", func(t *testing.T) {
		if stemcellCID == "" {
			t.Fatal("stemcellCID empty — step02 must have passed")
		}
		if networkCID == "" {
			t.Fatal("networkCID empty — step02a must have passed")
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
					"bridge": effBridge,
				},
			},
		}
		result := cpiCall(t, client, ctx, "step03", "create_vm",
			e.agentID, stemcellCID, cloudProps, networks, []any{}, map[string]any{})
		vmCID = resultString(t, "step03_create_vm", result)
		if vmCID == "" {
			t.Fatalf("create_vm returned empty CID")
		}
		t.Logf("vm_cid=%q", vmCID)
		assertVMExists(t, verifier, vmCID, true)
		t.Logf("verify: vm %q exists on PVE", vmCID)
	})

	// --- Step 4: has_vm ---
	t.Run("step04_has_vm_true", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		result := cpiCall(t, client, ctx, "step04", "has_vm", vmCID)
		if !resultBool(t, "step04_has_vm_true", result) {
			t.Fatalf("has_vm(%q) returned false, expected true", vmCID)
		}
	})

	// --- Step 5: set_vm_metadata ---
	t.Run("step05_set_vm_metadata", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		cpiCall(t, client, ctx, "step05", "set_vm_metadata", vmCID, map[string]any{
			"deployment": "lifecycle-network-bridge-test",
			"director":   "ocfp-lifecycle-harness",
		})
		t.Logf("set_vm_metadata=ok")
	})

	// --- Step 6: create_disk ---
	t.Run("step06_create_disk", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
		result := cpiCall(t, client, ctx, "step06", "create_disk",
			e.diskSizeMiB, map[string]any{}, vmCID)
		diskCID = resultString(t, "step06_create_disk", result)
		if diskCID == "" {
			t.Fatalf("create_disk returned empty CID")
		}
		t.Logf("disk_cid=%q", diskCID)
		assertVolumeExists(t, verifier, diskCID, true)
		t.Logf("verify: volume %q exists on PVE", diskCID)
	})

	// --- Step 6a: has_disk ---
	t.Run("step06a_has_disk_true", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step06a", "has_disk", diskCID)
		if !resultBool(t, "step06a_has_disk_true", result) {
			t.Fatalf("has_disk(%q) returned false post-create, expected true", diskCID)
		}
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

	// --- Step 8: get_disks ---
	t.Run("step08_get_disks", func(t *testing.T) {
		if vmCID == "" || diskCID == "" {
			t.Fatal("vmCID or diskCID empty — steps 03/06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step08", "get_disks", vmCID)
		disks := resultStringSlice(t, "step08_get_disks", result)
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
			diskCID, map[string]any{"deployment": "lifecycle-network-bridge-test"})
		snapshotCID = resultString(t, "step09_snapshot_disk", result)
		if snapshotCID == "" {
			t.Fatalf("snapshot_disk returned empty CID")
		}
		t.Logf("snapshot_cid=%q", snapshotCID)
	})

	// --- Step 10: delete_snapshot ---
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
		t.Logf("delete_disk=ok")
	})

	// --- Step 12a: has_disk (post-delete) ---
	t.Run("step12a_has_disk_false", func(t *testing.T) {
		if diskCID == "" {
			t.Fatal("diskCID empty — step06 must have passed")
		}
		result := cpiCall(t, client, ctx, "step12a", "has_disk", diskCID)
		if resultBool(t, "step12a_has_disk_false", result) {
			t.Fatalf("has_disk(%q) returned true post-delete, expected false", diskCID)
		}
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
		result := cpiCall(t, client, ctx, "step13_post", "has_vm", vmCID)
		if !resultBool(t, "step13_reboot_vm_soft/has_vm", result) {
			t.Fatalf("has_vm returned false after soft reboot_vm — VM %q gone", vmCID)
		}
		t.Logf("reboot_vm(soft)=ok")
	})

	// --- Step 14: reboot_vm (hard) ---
	t.Run("step14_reboot_vm_hard", func(t *testing.T) {
		if vmCID == "" {
			t.Fatal("vmCID empty — step03 must have passed")
		}
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
			Context:   map[string]any{"request_id": fmt.Sprintf("reboot_vm-hard-%d", time.Now().UnixNano())},
		}
		resp, err := hardClient.Call(ctx, req)
		if err != nil {
			t.Fatalf("[step14] reboot_vm(hard): RPC error: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("[step14] reboot_vm(hard): CPI error [%s]: %s", resp.Error.Type, resp.Error.Message)
		}
		result := cpiCall(t, client, ctx, "step14_post", "has_vm", vmCID)
		if !resultBool(t, "step14_reboot_vm_hard/has_vm", result) {
			t.Fatalf("has_vm returned false after hard reboot_vm — VM %q gone", vmCID)
		}
		t.Logf("reboot_vm(hard)=ok")
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
		if strings.Contains(deletedStemcellCID, ":") {
			assertVolumeExists(t, verifier, deletedStemcellCID, false)
			t.Logf("verify: stemcell volume %q absent from PVE", deletedStemcellCID)
		}
	})

	// --- Step 16a: delete_network (bridge mode) ---
	t.Run("step16a_delete_network_bridge", func(t *testing.T) {
		if networkCID == "" {
			t.Fatal("networkCID empty — step02a must have passed")
		}
		deletedNetCID := networkCID
		cpiCall(t, client, ctx, "step16a", "delete_network", networkCID)
		networkCID = ""
		t.Logf("delete_network=ok")

		// Out-of-band: bridge must be gone.
		assertBridgeExists(t, verifier, deletedNetCID, false)
		t.Logf("verify: bridge %q gone from PVE node %q", deletedNetCID, e.pveNode)
	})
}
