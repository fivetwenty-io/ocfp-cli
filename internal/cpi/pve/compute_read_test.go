package pve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	pveapierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

// readFakePVE answers GetCtx from canned responses and canned errors keyed by
// path, the way the apiclient answers the read calls GetInstance and
// ListInstances make. A path with neither fails with errFakeNoCannedResponse.
type readFakePVE struct {
	fakePVEClient

	errs map[string]error
	gets []string
}

func (f *readFakePVE) GetCtx(ctx context.Context, path string, params map[string]interface{}) (interface{}, error) {
	f.gets = append(f.gets, path)

	if err, ok := f.errs[path]; ok {
		return nil, err
	}

	return f.fakePVEClient.GetCtx(ctx, path, params)
}

// pveAPIError builds the error the apiclient returns for a non-2xx answer
// whose JSON body carries message.
func pveAPIError(status int, message string) error {
	body := fmt.Sprintf(`{"data":null,"message":%q}`, message)

	return fmt.Errorf("API request failed: %w", pveapierrors.ParseAPIError(status, []byte(body)))
}

// configMissing is PVE's answer to a request for a VM that has no config on
// the node the request names.
func configMissing(node string, vmid int) error {
	return pveAPIError(500, fmt.Sprintf("Configuration file 'nodes/%s/qemu-server/%d.conf' does not exist\n", node, vmid))
}

// newReadTestManager builds a compute manager over fake. An empty node makes
// the manager find a VM's node by probing every node /nodes lists.
func newReadTestManager(fake *readFakePVE, node string) *ComputeManager {
	client := &Client{config: &Config{Node: node}, pveClient: fake}
	client.compute = &ComputeManager{client: client}

	return client.compute
}

func TestGetInstance_MissingVMConfigIsVMNotFound(t *testing.T) {
	t.Parallel()

	for name, failing := range map[string]string{
		"status": "/nodes/pve1/qemu/100/status/current",
		"config": "/nodes/pve1/qemu/100/config",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := &readFakePVE{
				fakePVEClient: fakePVEClient{getResponses: map[string]interface{}{
					"/nodes/pve1/qemu/100/status/current": map[string]interface{}{"status": "running"},
					"/nodes/pve1/qemu/100/config":         map[string]interface{}{"name": "lab-bastion"},
				}},
				errs: map[string]error{failing: configMissing("pve1", 100)},
			}

			_, err := newReadTestManager(fake, "pve1").GetInstance(context.Background(), "100")
			if !errors.Is(err, ErrVMNotFound) {
				t.Fatalf("GetInstance error = %v, want ErrVMNotFound", err)
			}

			if strings.Contains(err.Error(), "Configuration file") {
				t.Fatalf("GetInstance error %q repeats PVE's message", err)
			}
		})
	}
}

func TestGetInstance_OtherFailuresAreNotVMNotFound(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]error{
		"server error":         pveAPIError(500, "unable to read status: got timeout"),
		"another VM's config":  configMissing("pve1", 101),
		"forbidden":            pveAPIError(403, "Configuration file 'nodes/pve1/qemu-server/100.conf' does not exist"),
		"a longer message":     pveAPIError(500, "Configuration file 'nodes/pve1/qemu-server/100.conf' does not exist, and SECRET123"),
		"an error with no API": errors.New("Configuration file 'nodes/pve1/qemu-server/100.conf' does not exist"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := &readFakePVE{errs: map[string]error{"/nodes/pve1/qemu/100/status/current": err}}

			_, got := newReadTestManager(fake, "pve1").GetInstance(context.Background(), "100")
			if got == nil || errors.Is(got, ErrVMNotFound) {
				t.Fatalf("GetInstance error = %v, want a failure that is not ErrVMNotFound", got)
			}
		})
	}
}
