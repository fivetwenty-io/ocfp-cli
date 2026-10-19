package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"context"
	"google.golang.org/api/option"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParityImageScopeAndFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/projects/images/global/images/family/ubuntu") {
			_, _ = w.Write([]byte(`{"name":"ubuntu-v1","selfLink":"projects/images/global/images/ubuntu-v1"}`))
			return
		}
		http.Error(w, `{"error":{"code":403,"message":"denied"}}`, 403)
	}))
	defer server.Close()
	api, err := compute.NewImagesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &ComputeManager{client: &Client{clientsLoaded: true, imagesClient: api, config: &Config{ProjectID: "p"}}}
	image, err := m.GetImage(context.Background(), "https://www.googleapis.com/compute/v1/projects/images/global/images/family/ubuntu")
	if err != nil || image.ID != "projects/images/global/images/ubuntu-v1" {
		t.Fatalf("image=%v err=%v", image, err)
	}
	calls = 0
	if _, err := m.GetImage(context.Background(), "private-image"); err == nil {
		t.Fatal("ignored permission failure")
	}
	if calls != 1 {
		t.Fatalf("permission failure queried %d projects", calls)
	}
}
