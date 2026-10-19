package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"encoding/json"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/encoding/protojson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bootTestManager(t *testing.T, handler http.HandlerFunc) *ComputeManager {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	opts := []option.ClientOption{option.WithEndpoint(server.URL), option.WithoutAuthentication()}
	vm, err := compute.NewInstancesRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vm.Close() })
	disk, err := compute.NewDisksRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	images, err := compute.NewImagesRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = images.Close() })
	snaps, err := compute.NewSnapshotsRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snaps.Close() })
	return &ComputeManager{client: &Client{clientsLoaded: true, instancesClient: vm, disksClient: disk, imagesClient: images, snapshotsClient: snaps, config: &Config{ProjectID: "p", Zone: "r-a"}}}
}
func bootVM() *computepb.Instance {
	return &computepb.Instance{Id: proto(uint64(42)), Name: proto("vm"), Zone: proto("projects/p/zones/r-b"), Status: proto("TERMINATED"), Disks: []*computepb.AttachedDisk{{Boot: proto(true), DeviceName: proto("root"), Source: proto("projects/p/zones/r-b/disks/old"), AutoDelete: proto(false), Interface: proto("SCSI")}, {Source: proto("projects/p/zones/r-b/disks/data"), DeviceName: proto("ocfp-bastion-data")}}, NetworkInterfaces: []*computepb.NetworkInterface{{Name: proto("nic0"), NetworkIP: proto("10.0.0.3"), Network: proto("projects/p/global/networks/net")}}, Metadata: &computepb.Metadata{Fingerprint: proto("fp"), Items: []*computepb.Items{{Key: proto("startup-script"), Value: proto("old script")}, {Key: proto("keep"), Value: proto("untouched")}}}}
}
func TestBootRefreshSwitchPreservesIdentityAndRestoresMetadata(t *testing.T) {
	vm := bootVM()
	attached := false
	metadataWrites := 0
	m := bootTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/operations/"):
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case strings.HasSuffix(r.URL.Path, "/detachDisk"):
			if r.URL.Query().Get("deviceName") != "root" {
				t.Error("wrong disk detached")
			}
			vm.Disks = vm.Disks[1:]
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case strings.HasSuffix(r.URL.Path, "/attachDisk"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["boot"] != true || body["autoDelete"] != false {
				t.Errorf("unsafe boot attach: %v", body)
			}
			vm.Disks = append([]*computepb.AttachedDisk{{Boot: proto(true), Source: proto(body["source"].(string)), DeviceName: proto("root")}}, vm.Disks...)
			attached = true
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case strings.HasSuffix(r.URL.Path, "/setMetadata"):
			var body computepb.Metadata
			data := json.NewDecoder(r.Body)
			_ = data.Decode(&body)
			vm.Metadata = &body
			metadataWrites++
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case strings.Contains(r.URL.Path, "/instances/vm"):
			b, _ := protojson.Marshal(vm)
			_, _ = w.Write(b)
		case strings.Contains(r.URL.Path, "/disks/"):
			_, _ = w.Write([]byte(`{"id":"99","name":"old","sizeGb":"20","type":"projects/p/zones/r-b/diskTypes/pd-balanced","architecture":"X86_64"}`))
		default:
			t.Errorf("unexpected %s", r.URL)
			http.Error(w, `{"error":{"code":404}}`, 404)
		}
	})
	ctx := context.Background()
	identity, err := m.InspectBoot(ctx, "projects/p/zones/r-b/instances/vm")
	if err != nil {
		t.Fatal(err)
	}
	identity.ReplacementUserData = "new script"
	identity.ReplacementFingerprint = "99"
	if err := m.SwitchBoot(ctx, identity, identity.RootVolumeID, "projects/p/zones/r-b/disks/new"); err != nil {
		t.Fatal(err)
	}
	if !attached || metadataWrites != 1 || len(vm.Disks) != 2 {
		t.Fatalf("switch state attached=%v metadata=%d disks=%d", attached, metadataWrites, len(vm.Disks))
	}
	if err := m.SwitchBoot(ctx, identity, "projects/p/zones/r-b/disks/new", identity.RootVolumeID); err != nil {
		t.Fatal(err)
	}
	var restored string
	for _, item := range vm.Metadata.GetItems() {
		if item.GetKey() == "startup-script" {
			restored = item.GetValue()
		}
	}
	if restored != "old script" {
		t.Errorf("rollback metadata=%q", restored)
	}
}
func TestBootRefreshRejectsRunningAndDriftedGuests(t *testing.T) {
	for _, drift := range []string{"running", "data", "network", "missing-startup", "foreign-pair", "disk-generation", "data-generation"} {
		t.Run(drift, func(t *testing.T) {
			vm := bootVM()
			mutations := 0
			m := bootTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "GET" {
					mutations++
					http.Error(w, `{"error":{"code":400}}`, 400)
					return
				}
				if strings.Contains(r.URL.Path, "/instances/") {
					b, _ := protojson.Marshal(vm)
					_, _ = w.Write(b)
				} else {
					id := "99"
					if (drift == "disk-generation" && vm.Disks[0].GetSource() == "projects/p/zones/r-b/disks/new") || (drift == "data-generation" && vm.GetDescription() == "drifted" && strings.HasSuffix(r.URL.Path, "/disks/data")) {
						id = "100"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "old", "sizeGb": "20", "type": "pd-balanced"})
				}
			})
			ctx := context.Background()
			identity, err := m.InspectBoot(ctx, "projects/p/zones/r-b/instances/vm")
			if err != nil {
				t.Fatal(err)
			}
			identity.ReplacementUserData = "new"
			switch drift {
			case "data-generation":
				vm.Description = proto("drifted")
			case "missing-startup":
				identity.ReplacementUserData = ""
			case "running":
				vm.Status = proto("RUNNING")
			case "data":
				vm.Disks[1].Source = proto("other")
			case "network":
				vm.NetworkInterfaces[0].NetworkIP = proto("10.0.0.8")
			}
			to := "projects/p/zones/r-b/disks/new"
			from := identity.RootVolumeID
			if drift == "disk-generation" {
				from = to
				to = identity.RootVolumeID
				vm.Disks[0].Source = proto(from)
			}
			if drift == "foreign-pair" {
				from = "projects/p/zones/r-b/disks/unrelated"
				vm.Disks[0].Source = proto(from)
			}
			if err := m.SwitchBoot(ctx, identity, from, to); err == nil {
				t.Fatal("unsafe switch accepted")
			}
			if mutations != 0 {
				t.Fatal("mutated drifted VM")
			}
		})
	}
}

func TestBootRefreshPreparationRecoversOnlyReadyOwnedDisk(t *testing.T) {
	for _, scenario := range []string{"ready", "pending", "foreign", "architecture", "read-error"} {
		t.Run(scenario, func(t *testing.T) {
			vm := bootVM()
			mutations := 0
			m := bootTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "GET" {
					mutations++
					http.Error(w, `{"error":{"code":400}}`, 400)
					return
				}
				switch {
				case strings.Contains(r.URL.Path, "/instances/"):
					b, _ := protojson.Marshal(vm)
					_, _ = w.Write(b)
				case strings.HasSuffix(r.URL.Path, "/disks/data"):
					_, _ = w.Write([]byte(`{"id":"88","sizeGb":"64"}`))
				case strings.Contains(r.URL.Path, "/images/"):
					architecture := "X86_64"
					if scenario == "architecture" {
						architecture = "ARM64"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"name": "image", "selfLink": "projects/p/global/images/image", "diskSizeGb": "10", "architecture": architecture})
				case strings.HasSuffix(r.URL.Path, "/disks/old"):
					_, _ = w.Write([]byte(`{"id":"99","sizeGb":"20","architecture":"X86_64","type":"projects/p/zones/r-b/diskTypes/pd-balanced"}`))
				default:
					if scenario == "read-error" {
						http.Error(w, `{"error":{"code":403}}`, 403)
						return
					}
					status := "READY"
					token := "token"
					if scenario == "pending" {
						status = "CREATING"
					}
					if scenario == "foreign" {
						token = "other"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"type": "projects/p/zones/r-b/diskTypes/pd-balanced", "id": "77", "name": bootTokenName("token", "boot"), "sourceImage": "https://www.googleapis.com/compute/v1/projects/p/global/images/image", "sizeGb": "20", "status": status, "labels": map[string]string{"ocfp-refresh-token": token}})
				}
			})
			identity, err := m.InspectBoot(context.Background(), "projects/p/zones/r-b/instances/vm")
			if err != nil {
				t.Fatal(err)
			}
			volume, err := m.PrepareBoot(context.Background(), &cpi.BootRefreshRequest{Identity: *identity, Image: "projects/p/global/images/image", Token: "token"})
			if scenario == "ready" {
				if err != nil || volume == nil {
					t.Fatalf("recovery: %v %v", volume, err)
				}
			} else if err == nil {
				t.Fatal("unsafe replacement adopted")
			}
			if mutations != 0 {
				t.Fatal("unexpected mutation")
			}
		})
	}
}

func TestBootRecoverySnapshotRejectsForeignAndPending(t *testing.T) {
	for _, scenario := range []string{"ready", "pending", "foreign", "read-error", "generation", "running", "changed-root"} {
		t.Run(scenario, func(t *testing.T) {
			vm := bootVM()
			mutations := 0
			m := bootTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "GET" {
					mutations++
					http.Error(w, `{"error":{"code":400}}`, 400)
					return
				}
				switch {
				case strings.Contains(r.URL.Path, "/instances/"):
					b, _ := protojson.Marshal(vm)
					_, _ = w.Write(b)
				case strings.Contains(r.URL.Path, "/disks/"):
					_, _ = w.Write([]byte(`{"id":"99","sizeGb":"20"}`))
				default:
					if scenario == "read-error" {
						http.Error(w, `{"error":{"code":403}}`, 403)
						return
					}
					status := "READY"
					source := "projects/p/zones/r-b/disks/old"
					if scenario == "pending" {
						status = "CREATING"
					}
					if scenario == "foreign" {
						source = "projects/p/zones/r-b/disks/other"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"name": bootTokenName("token", "recovery"), "sourceDiskId": map[bool]string{true: "100", false: "99"}[scenario == "generation"], "sourceDisk": source, "status": status, "labels": map[string]string{"ocfp-refresh-token": "token"}})
				}
			})
			identity, err := m.InspectBoot(context.Background(), "projects/p/zones/r-b/instances/vm")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "running" {
				vm.Status = proto("RUNNING")
			}
			if scenario == "changed-root" {
				vm.Disks[0].Source = proto("projects/p/zones/r-b/disks/other")
			}
			name, err := m.SnapshotBoot(context.Background(), identity, "token")
			if scenario == "ready" {
				if err != nil || name == "" {
					t.Fatalf("snapshot recovery: %s %v", name, err)
				}
			} else if err == nil {
				t.Fatal("unsafe snapshot adopted")
			}
			if mutations != 0 {
				t.Fatal("unexpected mutation")
			}
		})
	}
}

func TestBootRefreshCreatesDiskAndSnapshotWithStableTokens(t *testing.T) {
	vm := bootVM()
	created := false
	var requests []map[string]any
	var tokens []string
	m := bootTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/operations/"):
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case r.Method == "POST":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			requests = append(requests, body)
			if strings.HasSuffix(r.URL.Path, "/disks") {
				created = true
			}
			tokens = append(tokens, r.URL.Query().Get("requestId"))
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case strings.Contains(r.URL.Path, "/instances/"):
			b, _ := protojson.Marshal(vm)
			_, _ = w.Write(b)
		case strings.HasSuffix(r.URL.Path, "/disks/data"):
			_, _ = w.Write([]byte(`{"id":"88","sizeGb":"64"}`))
		case strings.Contains(r.URL.Path, "/images/"):
			_, _ = w.Write([]byte(`{"name":"image","selfLink":"projects/p/global/images/image","diskSizeGb":"30","architecture":"X86_64"}`))
		case strings.HasSuffix(r.URL.Path, "/disks/old"):
			_, _ = w.Write([]byte(`{"id":"99","sizeGb":"20","architecture":"X86_64","type":"projects/p/zones/r-b/diskTypes/pd-balanced","diskEncryptionKey":{"kmsKeyName":"projects/p/locations/r/keyRings/k/cryptoKeys/key"}}`))
		default:
			if created && strings.Contains(r.URL.Path, "/disks/") {
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "projects/p/zones/r-b/diskTypes/pd-balanced", "diskEncryptionKey": map[string]string{"kmsKeyName": "projects/p/locations/r/keyRings/k/cryptoKeys/key"}, "id": "77", "sourceImage": "projects/p/global/images/image", "sizeGb": "30", "status": "READY", "labels": map[string]string{"ocfp-refresh-token": "token"}})
				return
			}
			http.Error(w, `{"error":{"code":404}}`, 404)
		}
	})
	ctx := context.Background()
	identity, err := m.InspectBoot(ctx, "projects/p/zones/r-b/instances/vm")
	if err != nil {
		t.Fatal(err)
	}
	volume, err := m.PrepareBoot(ctx, &cpi.BootRefreshRequest{Identity: *identity, Image: "projects/p/global/images/image", Token: "token"})
	if err != nil || volume == nil {
		t.Fatalf("prepare: %v %v", volume, err)
	}
	snapshot, err := m.SnapshotBoot(ctx, identity, "token")
	if err != nil || snapshot == "" {
		t.Fatalf("snapshot: %s %v", snapshot, err)
	}
	if len(requests) != 2 || len(tokens) != 2 {
		t.Fatalf("requests=%v", requests)
	}
	if tokens[0] != *bootRequestID("token") || tokens[1] != *bootRequestID("tokensnapshot") {
		t.Fatalf("unstable request IDs %v", tokens)
	}
	disk := requests[0]
	if disk["sizeGb"] != "30" || disk["type"] != "projects/p/zones/r-b/diskTypes/pd-balanced" {
		t.Fatalf("wrong disk properties %v", disk)
	}
	key, ok := disk["diskEncryptionKey"].(map[string]any)
	if !ok || key["kmsKeyName"] != "projects/p/locations/r/keyRings/k/cryptoKeys/key" {
		t.Fatalf("encryption lost %v", disk)
	}
	if requests[1]["sourceDisk"] != identity.RootVolumeID {
		t.Fatal("snapshot source changed")
	}
}

func TestBootRecoveryNeverAllocatesOrDeletesForeignDisk(t *testing.T) {
	for _, scenario := range []string{"absent", "foreign", "ready", "read-error", "generation"} {
		t.Run(scenario, func(t *testing.T) {
			vm := bootVM()
			mutations := 0
			m := bootTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "GET" {
					mutations++
					http.Error(w, `{"error":{"code":400}}`, 400)
					return
				}
				switch {
				case strings.Contains(r.URL.Path, "/instances/"):
					b, _ := protojson.Marshal(vm)
					_, _ = w.Write(b)
				case strings.HasSuffix(r.URL.Path, "/disks/old"), strings.HasSuffix(r.URL.Path, "/disks/data"):
					_, _ = w.Write([]byte(`{"id":"99","sizeGb":"20","type":"projects/p/zones/r-b/diskTypes/pd-balanced"}`))
				default:
					if scenario == "absent" {
						http.Error(w, `{"error":{"code":404}}`, 404)
						return
					}
					if scenario == "read-error" {
						http.Error(w, `{"error":{"code":403}}`, 403)
						return
					}
					token := "token"
					if scenario == "foreign" {
						token = "other"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "77", "type": "projects/p/zones/r-b/diskTypes/pd-balanced", "sizeGb": "20", "sourceImage": "projects/p/global/images/image", "labels": map[string]string{"ocfp-refresh-token": token}})
				}
			})
			ctx := context.Background()
			identity, err := m.InspectBoot(ctx, "projects/p/zones/r-b/instances/vm")
			if err != nil {
				t.Fatal(err)
			}
			volume, err := m.RecoverBoot(ctx, &cpi.BootRefreshRequest{Identity: *identity, Image: "projects/p/global/images/image", Token: "token"})
			switch scenario {
			case "absent":
				if err != nil || volume != nil {
					t.Fatalf("absence: %v %v", volume, err)
				}
			case "ready", "generation":
				if err != nil || volume == nil || volume.Fingerprint != "77" {
					t.Fatalf("recovery: %v %v", volume, err)
				}
			default:
				if err == nil || volume != nil {
					t.Fatalf("unsafe adoption: %v %v", volume, err)
				}
			}
			if scenario == "generation" {
				if err = m.DeleteBoot(ctx, volume.ID, "76"); err == nil {
					t.Fatal("deleted recreated disk")
				}
			}
			if mutations != 0 {
				t.Fatal("recovery unexpectedly mutated resources")
			}
		})
	}
}
