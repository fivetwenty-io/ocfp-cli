package gcp

import (
	"cloud.google.com/go/storage"
	"context"
	"encoding/json"
	"google.golang.org/api/option"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParityBucketGenerations(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			if r.URL.Query().Get("versions") != "true" {
				t.Error("listing only live objects")
			}
			_, _ = w.Write([]byte(`{"items":[{"name":"old","generation":"42"}]}`))
			return
		}
		if r.Method == "DELETE" {
			if r.URL.Query().Get("generation") != "42" {
				t.Error("deleting without generation")
			}
			deleted = true
			w.WriteHeader(204)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL)
	}))
	defer server.Close()
	api, err := storage.NewClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &StorageManager{client: &Client{clientsLoaded: true, storageClient: api}}
	empty, err := m.IsBucketEmpty(context.Background(), "bucket")
	if err != nil || empty {
		t.Fatalf("empty=%v err=%v", empty, err)
	}
	if err = m.EmptyBucket(context.Background(), "bucket"); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("generation not removed")
	}
}
func TestParityCredentialGroupExplicit(t *testing.T) {
	_, err := (&StorageManager{}).CreateCredentialsGroup(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatal("fake credentials success")
	}
}

func TestParityBucketPolicyInterfaces(t *testing.T) {
	var manager interface{} = &StorageManager{}
	if _, ok := manager.(interface {
		SetBucketVersioning(context.Context, string, bool) error
	}); !ok {
		t.Fatal("missing bucket versioning capability")
	}
	if _, ok := manager.(interface {
		SetBucketLifecycle(context.Context, string, int) error
	}); !ok {
		t.Fatal("missing bucket lifecycle capability")
	}
}

func TestParityBucketPolicyPreservesScopedRules(t *testing.T) {
	patched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"name":"bucket","metageneration":"7","lifecycle":{"rule":[{"action":{"type":"Delete"},"condition":{"daysSinceNoncurrentTime":12,"isLive":false,"matchesPrefix":["keep/"]}}]}}`))
			return
		}
		if r.Method == "PATCH" {
			if r.URL.Query().Get("ifMetagenerationMatch") != "7" {
				t.Error("missing concurrent policy guard")
			}
			var body struct {
				Lifecycle struct {
					Rules []json.RawMessage `json:"rule"`
				} `json:"lifecycle"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.Lifecycle.Rules) != 2 {
				t.Errorf("removed unrelated policy: %s", body.Lifecycle.Rules)
			}
			patched = true
			_, _ = w.Write([]byte(`{"name":"bucket"}`))
			return
		}
		t.Errorf("unexpected %s", r.Method)
	}))
	defer server.Close()
	api, err := storage.NewClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &StorageManager{client: &Client{clientsLoaded: true, storageClient: api}}
	if err := m.SetBucketLifecycle(context.Background(), "bucket", 30); err != nil {
		t.Fatal(err)
	}
	if !patched {
		t.Fatal("policy not written")
	}
}
