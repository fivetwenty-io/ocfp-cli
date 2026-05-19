package pve

import (
	"net/http"
	"strings"
	"testing"
)

// stubS3Handler returns a tiny HTTP handler that pretends to be an
// S3-compatible endpoint just enough to satisfy CreateBucket/HeadBucket/
// ListObjectsV2. It records request paths into seen so the caller can verify
// path-style addressing. Each response body is a hand-rolled XML literal —
// importing the full S3 protobuf models here would be overkill for routing
// assertions.
func stubS3Handler(t *testing.T, seen *[]string) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)

		w.Header().Set("Content-Type", "application/xml")

		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/bloc-"):
			// CreateBucket — 200 OK.
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, "/bloc-"):
			// HeadBucket — 200 OK.
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
			// ListObjectsV2 — empty list, IsTruncated=false.
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>bloc-mgmt-bosh</Name>
  <KeyCount>0</KeyCount>
  <IsTruncated>false</IsTruncated>
</ListBucketResult>`))
		case r.Method == http.MethodGet && r.URL.Path == "/":
			// ListBuckets.
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Buckets/>
  <Owner><ID>0</ID><DisplayName>stub</DisplayName></Owner>
</ListAllMyBucketsResult>`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}
