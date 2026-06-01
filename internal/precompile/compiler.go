package precompile

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Compiler orchestrates per-release resolution into pin Resolutions, populating
// the blobstore as needed. It is transport-agnostic: Director, S3, and HTTP are
// injected so the logic is unit-testable with fakes.
type Compiler struct {
	Director   Director
	S3         objectAPI
	HTTP       *http.Client
	Bucket     string // blobstore bucket for compiled CF tarballs
	Endpoint   string // RustFS https base for HTTPSURL (e.g. https://10.0.0.5:9000)
	Deployment string // no-VM compile deployment name
	WorkDir    string // temp dir for downloaded/exported tarballs
	Log        func(string, ...any)
}

func (c *Compiler) logf(format string, a ...any) {
	if c.Log != nil {
		c.Log(format, a...)
	}
}

// ResolveDirector pins each director release directly to its upstream compiled
// URL (the artifacts blobstore does not exist at create-env time). The sha is
// computed by streaming the upstream tarball unless already known.
func (c *Compiler) ResolveDirector(ctx context.Context, rels []Release, sc Stemcell, opts Options) ([]Resolution, error) {
	out := make([]Resolution, 0, len(rels))
	for _, r := range rels {
		if r.UpstreamCompiledURL == "" {
			return nil, fmt.Errorf("director release %s/%s: no upstream compiled build for %s; compile-local for the director is unsupported (no blobstore at create-env)",
				r.Name, r.Version, sc)
		}

		res := Resolution{Release: r, Source: SourceUpstream, URL: r.UpstreamCompiledURL, SHA: r.UpstreamCompiledSHA}
		if opts.DryRun {
			out = append(out, res)
			continue
		}
		if res.SHA == "" {
			sha, err := RemoteSHA256(ctx, c.HTTP, r.UpstreamCompiledURL)
			if err != nil {
				return nil, err
			}
			res.SHA = sha
		}
		c.logf("upstream: %s/%s", r.Name, r.Version)
		out = append(out, res)
	}
	return out, nil
}

// ResolveBlobstore resolves each release through present -> fetch-upstream ->
// compile-local, populating the blobstore and returning pin Resolutions whose
// URLs are RustFS path-style https. Compile-local runs as a single no-VM deploy
// over all releases that need it, then exports each tarball concurrently.
func (c *Compiler) ResolveBlobstore(ctx context.Context, rels []Release, sc Stemcell, opts Options) ([]Resolution, error) {
	resolved := make(map[string]Resolution, len(rels))
	var toCompile []Release

	for _, r := range rels {
		key := CompiledKey(r, sc)
		url := HTTPSURL(c.Endpoint, c.Bucket, key)

		if !opts.Force {
			sha, ok, err := HeadCompiled(ctx, c.S3, c.Bucket, key)
			if err != nil {
				return nil, err
			}
			if ok && sha != "" {
				c.logf("present: %s/%s", r.Name, r.Version)
				resolved[r.Name] = Resolution{Release: r, Source: SourcePresent, URL: url, SHA: sha}
				continue
			}
		}

		if r.UpstreamCompiledURL != "" {
			if opts.DryRun {
				resolved[r.Name] = Resolution{Release: r, Source: SourceUpstream, URL: url}
				continue
			}
			res, err := c.fetchUpstream(ctx, r, key, url)
			if err != nil {
				return nil, err
			}
			c.logf("fetched upstream: %s/%s", r.Name, r.Version)
			resolved[r.Name] = res
			continue
		}

		if opts.DryRun {
			resolved[r.Name] = Resolution{Release: r, Source: SourceCompiled, URL: url}
			continue
		}
		toCompile = append(toCompile, r)
	}

	if len(toCompile) > 0 {
		compiled, err := c.compileBatch(ctx, toCompile, sc, opts)
		if err != nil {
			return nil, err
		}
		for name, res := range compiled {
			resolved[name] = res
		}
	}

	// Preserve input order in the output.
	out := make([]Resolution, 0, len(rels))
	for _, r := range rels {
		res, ok := resolved[r.Name]
		if !ok {
			return nil, fmt.Errorf("internal: release %s/%s left unresolved", r.Name, r.Version)
		}
		out = append(out, res)
	}
	return out, nil
}

func (c *Compiler) fetchUpstream(ctx context.Context, r Release, key, url string) (Resolution, error) {
	tmp := filepath.Join(c.WorkDir, slug(r.Name)+"-"+slug(r.Version)+".tgz")
	if _, err := DownloadToFile(ctx, c.HTTP, r.UpstreamCompiledURL, tmp); err != nil {
		return Resolution{}, err
	}
	defer func() { _ = os.Remove(tmp) }()

	sha, err := UploadCompiledFile(ctx, c.S3, c.Bucket, key, tmp)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{Release: r, Source: SourceUpstream, URL: url, SHA: sha}, nil
}

// compileBatch uploads any missing source releases, runs one no-VM compile
// deploy, then exports + uploads each compiled tarball with bounded concurrency.
func (c *Compiler) compileBatch(ctx context.Context, rels []Release, sc Stemcell, opts Options) (map[string]Resolution, error) {
	for _, r := range rels {
		present, err := c.Director.ReleasePresent(ctx, r.Name, r.Version)
		if err != nil {
			return nil, err
		}
		if present {
			c.logf("source present: %s/%s", r.Name, r.Version)
			continue
		}
		if r.UpstreamSourceURL == "" {
			return nil, fmt.Errorf("release %s/%s: no source url to upload for compilation", r.Name, r.Version)
		}
		c.logf("uploading source: %s/%s", r.Name, r.Version)
		if err := c.Director.UploadRelease(ctx, r.UpstreamSourceURL, r.UpstreamSourceSHA); err != nil {
			return nil, err
		}
	}

	man, err := RenderCompileManifest(c.Deployment, rels, sc)
	if err != nil {
		return nil, err
	}
	manPath := filepath.Join(c.WorkDir, c.Deployment+".yml")
	if err := os.WriteFile(manPath, man, 0o600); err != nil {
		return nil, fmt.Errorf("writing compile manifest: %w", err)
	}

	c.logf("compiling %d release(s) against %s (no-VM deploy)", len(rels), sc)
	if err := c.Director.Deploy(ctx, c.Deployment, manPath); err != nil {
		return nil, err
	}

	results := make(map[string]Resolution, len(rels))
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(opts.concurrency())
	for _, r := range rels {
		g.Go(func() error {
			key := CompiledKey(r, sc)
			url := HTTPSURL(c.Endpoint, c.Bucket, key)

			dir, err := os.MkdirTemp(c.WorkDir, "export-")
			if err != nil {
				return fmt.Errorf("mktemp for %s: %w", r.Name, err)
			}
			defer func() { _ = os.RemoveAll(dir) }()

			tarball, err := c.Director.ExportRelease(gctx, c.Deployment, r.Name, r.Version, sc, dir)
			if err != nil {
				return err
			}
			sha, err := UploadCompiledFile(gctx, c.S3, c.Bucket, key, tarball)
			if err != nil {
				return err
			}

			mu.Lock()
			results[r.Name] = Resolution{Release: r, Source: SourceCompiled, URL: url, SHA: sha}
			mu.Unlock()
			c.logf("compiled+uploaded: %s/%s", r.Name, r.Version)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}
