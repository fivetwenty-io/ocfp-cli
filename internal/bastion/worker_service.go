package bastion

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/workerunit"
)

var (
	// errWorkerBundleBloc reports a worker bundle issued for another bloc.
	errWorkerBundleBloc = errors.New("the worker bundle was issued for another bloc")

	// ErrWorkerModeNoBundle reports --worker with no worker bundle named.
	ErrWorkerModeNoBundle = errors.New("--worker installs the bloc worker from the worker bundle, and none is named")
)

// installWorkerService installs the bloc's worker on the bastion from the
// worker bundle that bastion.worker.bundle or OCFP_WORKER_BUNDLE names,
// and starts it as a service. With no bundle named it does nothing.
func (m *Manager) installWorkerService(ctx context.Context) error {
	return m.installWorkerFrom(ctx, os.Getenv)
}

// runWorkerOnlyMode installs and starts the bloc worker and does nothing
// else, for a bastion that a full init provisioned before it had a worker
// bundle. The full phase list skips the worker when no bundle is named,
// but this mode is asked for by name, so it refuses instead.
func (m *Manager) runWorkerOnlyMode(ctx context.Context) error {
	m.log.Info("Worker mode: installing the bloc worker only")

	bundlePath, err := m.config.Bastion.Worker.BundlePath(os.Getenv)
	if err != nil {
		return fmt.Errorf("installing the bloc worker: %w", err)
	}

	if bundlePath == "" {
		return fmt.Errorf("%w: set bastion.worker.bundle in the bloc's config, or %s, to the bundle's path",
			ErrWorkerModeNoBundle, config.EnvWorkerBundle)
	}

	err = m.installWorkerService(ctx)
	if err != nil {
		return fmt.Errorf("bloc worker install failed: %w", err)
	}

	m.logFinished("Bloc worker installed and started", "bloc", m.config.Name)

	return nil
}

// installWorkerFrom is installWorkerService with the environment lookup
// passed in. It probes the bastion first, with a read-only script, so the
// bundle is read knowing the bastion's architecture, and a binary that is
// not the one the join file lists for it is refused before anything is
// uploaded or changed. A dry run never probes, so it checks only that the
// binary is listed under some architecture. It restarts the worker only
// when a step changed something.
func (m *Manager) installWorkerFrom(ctx context.Context, getenv func(string) string) error {
	bloc := m.config.Name

	bundlePath, err := m.config.Bastion.Worker.BundlePath(getenv)
	if err != nil {
		return fmt.Errorf("installing the bloc worker: %w", err)
	}

	if bundlePath == "" {
		m.log.Infow("Skipping the bloc worker install: no worker bundle is configured",
			"bloc", bloc, "setting", "bastion.worker.bundle", "env", config.EnvWorkerBundle)

		return nil
	}

	_, err = os.Stat(bundlePath)
	if err != nil {
		return fmt.Errorf("reading the worker bundle %s: %w", bundlePath, err)
	}

	dryRun := m.options != nil && m.options.DryRun

	var (
		probe workerunit.Probe
		arch  string
	)

	if !dryRun {
		probe, err = m.probeWorkerHost(ctx, bloc)
		if err != nil {
			return err
		}

		arch, err = workerunit.Platform(probe.Arch)
		if err != nil {
			return fmt.Errorf("installing the bloc worker: %w", err)
		}
	}

	staging, err := os.MkdirTemp("", "ocfp-worker-bundle-")
	if err != nil {
		return fmt.Errorf("making a private directory for the worker bundle: %w", err)
	}

	defer func() { _ = os.RemoveAll(staging) }()

	bundle, err := workerunit.ExtractBundle(bundlePath, staging, arch)
	if err != nil {
		return fmt.Errorf("reading the worker bundle %s: %w", bundlePath, err)
	}

	if bundle.Join.Bloc != bloc {
		return fmt.Errorf("%w: %s holds a join file for %s, and this bastion serves %s",
			errWorkerBundleBloc, bundlePath, bundle.Join.Bloc, bloc)
	}

	if dryRun {
		m.log.Infow("DRY RUN: Would install and start the bloc worker",
			"bloc", bloc, "bundle", bundlePath, "binary_sha256", bundle.BinarySHA256,
			"version", bundle.Join.Binary.Version, "path", workerunit.BinaryPath)

		return nil
	}

	return m.installWorkerBundle(ctx, bloc, bundle, probe)
}

// installWorkerBundle installs a bundle that was read and checked for the
// bastion that probe describes.
func (m *Manager) installWorkerBundle(
	ctx context.Context, bloc string, bundle workerunit.Bundle, probe workerunit.Probe,
) error {
	changed, err := m.installWorkerFiles(ctx, bloc, bundle, probe)
	if err != nil {
		return err
	}

	unitChanged, err := m.installWorkerUnit(ctx, bloc, probe)
	if err != nil {
		return err
	}

	err = m.startWorker(ctx, bloc, changed || unitChanged)
	if err != nil {
		return err
	}

	return m.reportWorkerHostKey(bloc, probe)
}

// probeWorkerHost reads the operator, the platform, the installed sums,
// and the host key from the bastion.
func (m *Manager) probeWorkerHost(ctx context.Context, bloc string) (workerunit.Probe, error) {
	cmd, err := workerunit.ProbeCommand(bloc)
	if err != nil {
		return workerunit.Probe{}, fmt.Errorf("installing the bloc worker: %w", err)
	}

	result, err := m.sshClient.ExecuteCommand(ctx, cmd)
	if err != nil {
		return workerunit.Probe{}, workerStepError("probing the bastion for the bloc worker", result, err)
	}

	if result == nil {
		return workerunit.Probe{}, fmt.Errorf("probing the bastion for the bloc worker: %w", workerunit.ErrProbe)
	}

	probe, err := workerunit.ParseProbe(result.Stdout)
	if err != nil {
		return workerunit.Probe{}, fmt.Errorf("probing the bastion for the bloc worker: %w", err)
	}

	return probe, nil
}

// installWorkerFiles creates the worker's directory, uploads the join file
// and the binary when their sums differ from the installed ones, and
// writes the loopback key, the signed attestation, and the record. It
// reports whether any of them changed.
func (m *Manager) installWorkerFiles(
	ctx context.Context, bloc string, bundle workerunit.Bundle, probe workerunit.Probe,
) (bool, error) {
	dirCmd, err := workerunit.DirectoryCommand(bloc, probe)
	if err != nil {
		return false, fmt.Errorf("installing the bloc worker: %w", err)
	}

	changed, err := m.runWorkerStep(ctx, "creating the worker directory", dirCmd)
	if err != nil {
		return false, err
	}

	if probe.JoinSHA256 != bundle.JoinSHA256 {
		upload, err := m.uploadWorkerFile(ctx, bloc, bundle.JoinPath, workerunit.JoinUploadPath,
			workerunit.JoinFileCommand, bundle.JoinSHA256, "the join file", true)
		if err != nil {
			return false, err
		}

		changed = changed || upload
	}

	if probe.BinarySHA256 != bundle.BinarySHA256 {
		upload, err := m.uploadWorkerFile(ctx, bloc, bundle.BinaryPath, workerunit.BinaryUploadPath,
			workerunit.BinaryCommand, bundle.BinarySHA256, "the worker binary", false)
		if err != nil {
			return false, err
		}

		changed = changed || upload
	}

	record, err := workerunit.NewRecord(bloc, probe.User)
	if err != nil {
		return false, fmt.Errorf("installing the bloc worker: %w", err)
	}

	steps := []struct {
		what  string
		build func() (string, error)
	}{
		{"writing the worker's loopback key", func() (string, error) { return workerunit.LoopbackCommand(bloc) }},
		{"preparing and signing the join request", func() (string, error) { return workerunit.PrepareCommand(bloc) }},
		{"writing the bastion record", func() (string, error) { return workerunit.RecordCommand(bloc, record) }},
	}

	for _, step := range steps {
		cmd, err := step.build()
		if err != nil {
			return false, fmt.Errorf("%s: %w", step.what, err)
		}

		stepChanged, err := m.runWorkerStep(ctx, step.what, cmd)
		if err != nil {
			return false, err
		}

		changed = changed || stepChanged
	}

	return changed, nil
}

// uploadWorkerFile sends local to its upload path in the worker's
// directory and runs the command that checks it against sha and moves it
// into place. A secret file, which the join file is because it holds the
// join key, travels only over SFTP or SCP and never in a command line.
func (m *Manager) uploadWorkerFile(
	ctx context.Context,
	bloc, local string,
	uploadPath func(string) (string, error),
	install func(string, string) (string, error),
	sha, what string,
	secret bool,
) (bool, error) {
	remote, err := uploadPath(bloc)
	if err != nil {
		return false, fmt.Errorf("uploading %s: %w", what, err)
	}

	cmd, err := install(bloc, sha)
	if err != nil {
		return false, fmt.Errorf("uploading %s: %w", what, err)
	}

	err = m.sshClient.TransferFile(ctx, local, remote, ssh.TransferOptions{Verify: true, Secret: secret})
	if err != nil {
		return false, fmt.Errorf("uploading %s to %s: %w", what, remote, err)
	}

	m.log.Infow("Uploaded a bloc worker file", "bloc", bloc, "file", what, "sha256", sha)

	return m.runWorkerStep(ctx, "installing "+what, cmd)
}

// installWorkerUnit renders and installs the worker's unit for the
// operator, and reports whether it changed.
func (m *Manager) installWorkerUnit(ctx context.Context, bloc string, probe workerunit.Probe) (bool, error) {
	unit, err := workerunit.Render(bloc, workerunit.UnitParams{User: probe.User, Home: probe.Home})
	if err != nil {
		return false, fmt.Errorf("rendering %s for %s: %w", workerunit.TemplateName, probe.User, err)
	}

	cmd, err := workerunit.InstallCommand(bloc, unit)
	if err != nil {
		return false, fmt.Errorf("installing the bloc worker unit: %w", err)
	}

	return m.runWorkerStep(ctx, "installing and enabling "+workerunit.UnitPath, cmd)
}

// startWorker restarts the worker when something changed and otherwise
// starts it, which leaves a running worker as it is.
func (m *Manager) startWorker(ctx context.Context, bloc string, restart bool) error {
	cmd, err := workerunit.StartCommand(bloc, restart)
	if err != nil {
		return fmt.Errorf("starting the bloc worker: %w", err)
	}

	_, err = m.runWorkerStep(ctx, "starting the bloc worker", cmd)
	if err != nil {
		return err
	}

	unitName, _ := workerunit.UnitName(bloc)
	m.log.Infow("Bloc worker installed and started", "bloc", bloc, "unit", unitName, "restarted", restart)

	return nil
}

// reportWorkerHostKey shows the operator the bastion's host key
// fingerprint, which the workers panel asks for when it has no host key on
// record for the bloc.
func (m *Manager) reportWorkerHostKey(bloc string, probe workerunit.Probe) error {
	fingerprint, err := workerunit.HostKeyFingerprint(probe.HostKey)
	if err != nil {
		return fmt.Errorf("reading the bastion's host key: %w", err)
	}

	tail := workerunit.FingerprintTail(fingerprint)

	m.log.Infow("Bloc worker is asking to join",
		"bloc", bloc, "host_key_fingerprint", fingerprint, "approval_characters", tail)

	if m.options != nil && m.options.ProgressOut != nil {
		_, _ = fmt.Fprintf(m.options.ProgressOut,
			"\nThe worker for %s is asking to join. Approve it in the bloc's workers panel.\n"+
				"  Bastion host key: %s\n"+
				"  If the panel has no host key on record for this bloc, type the last 12 characters: %s\n",
			bloc, fingerprint, tail)
	}

	return nil
}

// runWorkerStep runs one of the worker's install scripts and reports
// whether it changed something.
func (m *Manager) runWorkerStep(ctx context.Context, what, cmd string) (bool, error) {
	result, err := m.sshClient.ExecuteCommand(ctx, cmd)
	if err != nil {
		return false, workerStepError(what, result, err)
	}

	if result == nil {
		return false, nil
	}

	return workerunit.Changed(result.Stdout), nil
}

// workerStepError wraps a failed step with the tail of its output. None of
// the scripts prints the join file, so the output never holds its key.
func workerStepError(what string, result *ssh.CommandResult, err error) error {
	if result != nil {
		if tail := extractTail(result.Stderr + "\n" + result.Stdout); tail != "" {
			return fmt.Errorf("%s: %w\n--- output ---\n%s", what, err, tail)
		}
	}

	return fmt.Errorf("%s: %w", what, err)
}
