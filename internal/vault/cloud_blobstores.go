package vault

// externalBlobstore reuses the provider-independent S3 export, TLS and bucket
// implementation. The native S3/GCS path remains the default when no artifacts
// state or explicit override exists.
func (a *AWSVaultProvider) externalBlobstore() (*PVEVaultProvider, error) {
	p := &PVEVaultProvider{BaseVaultProvider: a.BaseVaultProvider, Safe: a.Safe, PathBuilder: a.PathBuilder, logger: a.logger}

	return cloudExternalBlobstore(p, a.blobstoreOptions)
}
func (g *GCPVaultProvider) externalBlobstore() (*PVEVaultProvider, error) {
	p := &PVEVaultProvider{BaseVaultProvider: g.BaseVaultProvider, Safe: g.Safe, PathBuilder: g.PathBuilder, logger: g.logger}

	return cloudExternalBlobstore(p, g.blobstoreOptions)
}
func cloudExternalBlobstore(p *PVEVaultProvider, opts *PopulateOptions) (*PVEVaultProvider, error) {
	if opts != nil {
		p.BlobstoreEndpoint = opts.BlobstoreEndpoint
		p.BlobstoreMode = opts.BlobstoreMode
		p.BlobstoreRegion = opts.BlobstoreRegion
		p.BlobstoreAccessKey = opts.BlobstoreAccessKey

		p.BlobstoreSecretKey = opts.BlobstoreSecretKey
		if opts.DryRun {
			p.bucketEnsurer = noopBucketEnsurer{}
		}

		if opts.BlobstoreEndpoint != "" || opts.BlobstoreMode != "" {
			return p, nil
		}
	}

	src, err := ConfigureBlobstoresFromArtifactsState(p.loadStateManager(), p.BlocName)
	if err != nil {
		return nil, err
	}

	if src == nil {
		return nil, nil
	}
	// Leave source promotion to ConfigureBlobstores so operational metadata and
	// trust material follow the same tested path as PVE.
	return p, nil
}
