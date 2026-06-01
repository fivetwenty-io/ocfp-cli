package precompile

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeS3 is an in-memory objectAPI. objects maps key -> sha256 hex metadata.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]string // key -> sha256 hex
	puts    int
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: map[string]string{}} }

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha, ok := f.objects[*in.Key]
	if !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{Metadata: map[string]string{shaMetaKey: sha}}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	f.objects[*in.Key] = in.Metadata[shaMetaKey]
	return &s3.PutObjectOutput{}, nil
}

// fakeDirector records calls and produces real tarball files for export.
type fakeDirector struct {
	present  map[string]bool // "name/version" -> present
	uploaded []string
	deployed bool
	exportTo string // dir where it writes fake tarballs
}

func (d *fakeDirector) ReleasePresent(_ context.Context, name, version string) (bool, error) {
	return d.present[name+"/"+version], nil
}

func (d *fakeDirector) UploadRelease(_ context.Context, url, _ string) error {
	d.uploaded = append(d.uploaded, url)
	return nil
}

func (d *fakeDirector) Deploy(_ context.Context, _, _ string) error {
	d.deployed = true
	return nil
}

func (d *fakeDirector) ExportRelease(_ context.Context, _, name, version string, sc Stemcell, destDir string) (string, error) {
	fn := filepath.Join(destDir, name+"-"+version+"-"+sc.OS+"-"+sc.Version+"-ts.tgz")
	if err := os.WriteFile(fn, []byte("tarball:"+name), 0o600); err != nil {
		return "", err
	}
	return fn, nil
}

func newCompiler(t *testing.T, s3c objectAPI, dir Director) *Compiler {
	t.Helper()
	return &Compiler{
		Director:   dir,
		S3:         s3c,
		Bucket:     "dev-ocf-bosh",
		Endpoint:   "https://10.0.0.5:9000",
		Deployment: "dev-precompile-cf",
		WorkDir:    t.TempDir(),
	}
}

func TestResolveBlobstorePresent(t *testing.T) {
	s3c := newFakeS3()
	rel := Release{Name: "capi", Version: "1.235.0"}
	s3c.objects[CompiledKey(rel, DefaultStemcell)] = "cafe"

	c := newCompiler(t, s3c, &fakeDirector{present: map[string]bool{}})
	out, err := c.ResolveBlobstore(context.Background(), []Release{rel}, DefaultStemcell, Options{})
	if err != nil {
		t.Fatalf("ResolveBlobstore: %v", err)
	}
	if out[0].Source != SourcePresent {
		t.Errorf("source = %q, want present", out[0].Source)
	}
	if out[0].SHA != "sha256:cafe" {
		t.Errorf("sha = %q, want sha256:cafe", out[0].SHA)
	}
	if s3c.puts != 0 {
		t.Errorf("present path should not upload, got %d puts", s3c.puts)
	}
}

func TestResolveBlobstoreCompileLocal(t *testing.T) {
	s3c := newFakeS3()
	dir := &fakeDirector{present: map[string]bool{}}
	c := newCompiler(t, s3c, dir)

	rels := []Release{
		{Name: "capi", Version: "1.235.0", UpstreamSourceURL: "https://x/capi.tgz"},
		{Name: "uaa", Version: "78.14.0", UpstreamSourceURL: "https://x/uaa.tgz"},
	}
	out, err := c.ResolveBlobstore(context.Background(), rels, DefaultStemcell, Options{})
	if err != nil {
		t.Fatalf("ResolveBlobstore: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d resolutions, want 2", len(out))
	}
	for _, r := range out {
		if r.Source != SourceCompiled {
			t.Errorf("%s: source = %q, want compiled", r.Name, r.Source)
		}
		if r.SHA == "" || r.URL == "" {
			t.Errorf("%s: empty sha/url: %+v", r.Name, r)
		}
	}
	if !dir.deployed {
		t.Error("expected a no-VM compile deploy")
	}
	if len(dir.uploaded) != 2 {
		t.Errorf("expected 2 source uploads, got %d", len(dir.uploaded))
	}
	if s3c.puts != 2 {
		t.Errorf("expected 2 blobstore puts, got %d", s3c.puts)
	}
	// Output order matches input order.
	if out[0].Name != "capi" || out[1].Name != "uaa" {
		t.Errorf("output order not preserved: %s, %s", out[0].Name, out[1].Name)
	}
}

func TestResolveBlobstoreForceReuploadsPresent(t *testing.T) {
	s3c := newFakeS3()
	rel := Release{Name: "capi", Version: "1.235.0", UpstreamSourceURL: "https://x/capi.tgz"}
	s3c.objects[CompiledKey(rel, DefaultStemcell)] = "stale"

	dir := &fakeDirector{present: map[string]bool{}}
	c := newCompiler(t, s3c, dir)
	out, err := c.ResolveBlobstore(context.Background(), []Release{rel}, DefaultStemcell, Options{Force: true})
	if err != nil {
		t.Fatalf("ResolveBlobstore: %v", err)
	}
	if out[0].Source != SourceCompiled {
		t.Errorf("force should bypass present, got source %q", out[0].Source)
	}
}

func TestResolveDirectorDryRun(t *testing.T) {
	c := newCompiler(t, newFakeS3(), &fakeDirector{})
	rels := BOSHReleases(DefaultStemcell)
	out, err := c.ResolveDirector(context.Background(), rels, DefaultStemcell, Options{DryRun: true})
	if err != nil {
		t.Fatalf("ResolveDirector: %v", err)
	}
	for _, r := range out {
		if r.Source != SourceUpstream {
			t.Errorf("%s: source = %q, want upstream", r.Name, r.Source)
		}
		if r.URL == "" {
			t.Errorf("%s: empty url", r.Name)
		}
	}
}

func TestResolveDirectorRejectsNoUpstream(t *testing.T) {
	c := newCompiler(t, newFakeS3(), &fakeDirector{})
	rels := BOSHReleases(Stemcell{OS: "ubuntu-jammy", Version: "1.0"})
	if _, err := c.ResolveDirector(context.Background(), rels, Stemcell{OS: "ubuntu-jammy", Version: "1.0"}, Options{}); err == nil {
		t.Error("expected error when no upstream compiled build exists")
	}
}
