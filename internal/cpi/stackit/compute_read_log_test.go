package stackit

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/stackitcloud/stackit-sdk-go/core/oapierror"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
)

// captureStackitLog points the global logger at a temporary directory at
// debug level and returns a func that reads back everything written to it.
func captureStackitLog(t *testing.T) func() string {
	t.Helper()

	dir := t.TempDir()

	err := logger.Initialize(logger.Config{Level: "debug", Debug: true, LogDir: dir, BlocName: "logcheck", Command: "test"})
	if err != nil {
		t.Fatalf("initializing the logger: %v", err)
	}

	t.Cleanup(func() { _ = logger.Initialize(logger.Config{NoLog: true}) })

	return func() string {
		_ = logger.Sync()

		var all strings.Builder

		walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}

			all.Write(data)

			return nil
		})
		if walkErr != nil {
			t.Fatalf("reading the log: %v", walkErr)
		}

		return all.String()
	}
}

func newMockedComputeManager(nicsErr error) *ComputeManager {
	serverID := "srv-1"
	getServer := func(iaas.ApiGetServerRequest) (*iaas.Server, error) {
		return &iaas.Server{Id: &serverID}, nil
	}
	listNICs := func(iaas.ApiListServerNICsRequest) (*iaas.NICListResponse, error) {
		return nil, nicsErr
	}
	listIPs := func(iaas.ApiListPublicIPsRequest) (*iaas.PublicIpListResponse, error) {
		return nil, errors.New("public IP listing refused with SECRET123")
	}

	client := &Client{
		config: &Config{ProjectID: "proj", Region: "eu01"},
		iaasClient: &iaas.APIClient{DefaultAPI: &iaas.DefaultAPIServiceMock{
			GetServerExecuteMock:      &getServer,
			ListServerNICsExecuteMock: &listNICs,
			ListPublicIPsExecuteMock:  &listIPs,
		}},
	}

	return &ComputeManager{client: client}
}

// TestGetInstance_NeverLogsProviderErrorText drives the real GetInstance with
// a failing NIC lookup whose error carries a fake secret, and checks that
// only a safe description reaches the log.
func TestGetInstance_NeverLogsProviderErrorText(t *testing.T) {
	readLog := captureStackitLog(t)

	cases := map[string]struct {
		err  error
		want string
	}{
		"api error":     {&oapierror.GenericOpenAPIError{StatusCode: 503, ErrorMessage: "SECRET123 unavailable"}, "(HTTP 503)"},
		"plain error":   {errors.New("dial tcp: token SECRET123 rejected"), "(unclassified error)"},
		"timeout error": {context.DeadlineExceeded, "(timeout)"},
	}

	for name, tc := range cases {
		manager := newMockedComputeManager(tc.err)

		inst, err := manager.GetInstance(context.Background(), "srv-1")
		if err != nil {
			t.Fatalf("%s: GetInstance: %v, want the instance despite the NIC failure", name, err)
		}

		if inst == nil || inst.ID != "srv-1" {
			t.Fatalf("%s: GetInstance = %v, want srv-1", name, inst)
		}

		logged := readLog()

		if strings.Contains(logged, "SECRET123") {
			t.Fatalf("%s: the log carries provider error text:\n%s", name, logged)
		}

		want := "networking read for instance srv-1 failed " + tc.want
		if !strings.Contains(logged, want) {
			t.Fatalf("%s: the log lacks %q:\n%s", name, want, logged)
		}
	}
}

// TestPopulateInstanceNetworking_ReturnsTheFullError checks that the failing
// read still hands its caller the provider's error unchanged.
func TestPopulateInstanceNetworking_ReturnsTheFullError(t *testing.T) {
	t.Parallel()

	manager := newMockedComputeManager(errors.New("dial tcp: token SECRET123 rejected"))

	cli, err := manager.client.getIAASClient()
	if err != nil {
		t.Fatalf("getIAASClient: %v", err)
	}

	err = manager.populateInstanceNetworking(context.Background(), cli, "srv-1", nil)
	if err == nil || !strings.Contains(err.Error(), "SECRET123") {
		t.Fatalf("error = %v, want the provider's error unchanged", err)
	}
}
