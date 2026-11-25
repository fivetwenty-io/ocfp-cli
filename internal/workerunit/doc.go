// Package workerunit installs a bloc's worker on its bastion. It reads the
// worker bundle that the bloc's workers panel offers for download, and it
// renders the worker's systemd unit, its directory, its bastion record, its
// loopback SSH key, and the signing of its join request's attestation with
// the bastion's SSH host key. Bastion init runs what it renders, and both
// take the worker's names and paths from here.
package workerunit
