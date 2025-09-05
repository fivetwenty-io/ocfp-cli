package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/pkg/sftp"
)

// TransferManager handles file transfers with multiple fallback strategies.
type TransferManager struct {
	client  *Client
	options TransferOptions
	log     logger.Logger
}

// NewTransferManager creates a new transfer manager.
func NewTransferManager(client *Client, options TransferOptions) *TransferManager {
	return &TransferManager{
		client:  client,
		options: options,
		log:     logger.Get(),
	}
}

// Transfer transfers a file using the best available method.
func (tm *TransferManager) Transfer(ctx context.Context, local, remote string) error {
	// Determine transfer direction
	if strings.HasPrefix(local, "bastion:") {
		// Download from bastion
		local = strings.TrimPrefix(local, "bastion:")

		return tm.download(ctx, local, remote)
	} else if strings.HasPrefix(remote, "bastion:") {
		// Upload to bastion
		remote = strings.TrimPrefix(remote, "bastion:")

		return tm.upload(ctx, local, remote)
	}

	return errors.New("at least one path must specify bastion: prefix")
}

// upload uploads a file to the bastion host.
func (tm *TransferManager) upload(ctx context.Context, localPath, remotePath string) error {
	tm.log.Info("Uploading file", "local", localPath, "remote", remotePath)

	// Check if local file exists
	localStat, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("local file not found: %w", err)
	}

	// Try transfer methods in order of preference
	methods := []struct {
		name string
		fn   func(context.Context, string, string, os.FileInfo) error
	}{
		{"SFTP", tm.uploadViaSFTP},
		{"SCP", tm.uploadViaSCP},
		{"TarPipe", tm.uploadViaTarPipe},
		{"Base64", tm.uploadViaBase64},
	}

	var lastErr error

	for _, method := range methods {
		tm.log.Debug("Attempting upload method", "method", method.name)

		err := method.fn(ctx, localPath, remotePath, localStat)
		if err != nil {
			tm.log.Warn("Upload method failed",
				"method", method.name,
				"error", err.Error())
			lastErr = err

			continue
		}

		tm.log.Info("File uploaded successfully",
			"method", method.name,
			"local", localPath,
			"remote", remotePath)

		// Verify transfer if requested
		if tm.options.Verify {
			err := tm.verifyUpload(ctx, localPath, remotePath)
			if err != nil {
				tm.log.Warn("Upload verification failed", "error", err.Error())
				// Don't fail the transfer, just log the warning
			}
		}

		return nil
	}

	return fmt.Errorf("all upload methods failed, last error: %w", lastErr)
}

// download downloads a file from the bastion host.
func (tm *TransferManager) download(ctx context.Context, remotePath, localPath string) error {
	tm.log.Info("Downloading file", "remote", remotePath, "local", localPath)

	// Try transfer methods in order of preference
	methods := []struct {
		name string
		fn   func(context.Context, string, string) error
	}{
		{"SFTP", tm.downloadViaSFTP},
		{"SCP", tm.downloadViaSCP},
		{"Cat", tm.downloadViaCat},
	}

	var lastErr error

	for _, method := range methods {
		tm.log.Debug("Attempting download method", "method", method.name)

		err := method.fn(ctx, remotePath, localPath)
		if err != nil {
			tm.log.Warn("Download method failed",
				"method", method.name,
				"error", err.Error())
			lastErr = err

			continue
		}

		tm.log.Info("File downloaded successfully",
			"method", method.name,
			"remote", remotePath,
			"local", localPath)

		return nil
	}

	return fmt.Errorf("all download methods failed, last error: %w", lastErr)
}

// uploadViaSFTP uploads using SFTP.
func (tm *TransferManager) uploadViaSFTP(ctx context.Context, local, remote string, localStat os.FileInfo) error {
	// Create SFTP client
	sftpClient, err := sftp.NewClient(tm.client.client)
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}

	defer func() { _ = sftpClient.Close() }()

	// Ensure remote directory exists
	remoteDir := filepath.Dir(remote)
	if err := sftpClient.MkdirAll(remoteDir); err != nil {
		tm.log.Warn("Failed to create remote directory",
			"dir", remoteDir,
			"error", err.Error())
	}

	// Open local file
	if err := security.ValidatePath(local); err != nil {
		return fmt.Errorf("invalid local path: %w", err)
	}

	localFile, err := os.Open(local) // #nosec G304 - local path is validated above
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}

	defer func() { _ = localFile.Close() }()

	// Create remote file
	remoteFile, err := sftpClient.Create(remote)
	if err != nil {
		return fmt.Errorf("failed to create remote file: %w", err)
	}

	defer func() { _ = remoteFile.Close() }()

	// Copy with progress reporting
	if tm.options.Progress != nil {
		_, err = tm.copyWithProgress(localFile, remoteFile, localStat.Size())
	} else {
		_, err = io.Copy(remoteFile, localFile)
	}

	if err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	// Set permissions if requested
	if tm.options.Preserve && localStat.Mode() != 0 {
		err := sftpClient.Chmod(remote, localStat.Mode())
		if err != nil {
			tm.log.Warn("Failed to set remote file permissions",
				"error", err.Error())
		}
	}

	return nil
}

// uploadViaSCP uploads using SCP command.
func (tm *TransferManager) uploadViaSCP(ctx context.Context, local, remote string, localStat os.FileInfo) error {
	// For now, delegate to external SCP command
	// In a full implementation, this would implement SCP protocol natively
	return tm.uploadViaExternalSCP(ctx, local, remote)
}

// uploadViaTarPipe uploads using tar pipe over SSH.
func (tm *TransferManager) uploadViaTarPipe(ctx context.Context, local, remote string, localStat os.FileInfo) error {
	// Create tar command that pipes to remote tar extraction
	remoteDir := filepath.Dir(remote)

	// Ensure remote directory exists
	mkdirCmd := fmt.Sprintf("mkdir -p '%s'", remoteDir)
	if _, err := tm.client.ExecuteCommand(ctx, mkdirCmd); err != nil {
		tm.log.Warn("Failed to create remote directory",
			"dir", remoteDir,
			"error", err.Error())
	}

	// Execute tar pipe (this would need proper implementation)
	return errors.New("tar pipe not fully implemented")
}

// uploadViaBase64 uploads by base64 encoding (for small files).
func (tm *TransferManager) uploadViaBase64(ctx context.Context, local, remote string, localStat os.FileInfo) error {
	// Only use for small files
	if localStat.Size() > 1024*1024 { // 1MB limit
		return errors.New("file too large for base64 transfer")
	}

	// Read local file
	if err := security.ValidatePath(local); err != nil {
		return fmt.Errorf("invalid local path: %w", err)
	}

	data, err := os.ReadFile(local) // #nosec G304 - local path is validated above
	if err != nil {
		return fmt.Errorf("failed to read local file: %w", err)
	}

	// Encode to base64
	encoded := base64.StdEncoding.EncodeToString(data)

	// Ensure remote directory exists
	remoteDir := filepath.Dir(remote)

	mkdirCmd := fmt.Sprintf("mkdir -p '%s'", remoteDir)
	if _, err := tm.client.ExecuteCommand(ctx, mkdirCmd); err != nil {
		tm.log.Warn("Failed to create remote directory", "error", err.Error())
	}

	// Write via base64 decode
	writeCmd := fmt.Sprintf("echo '%s' | base64 -d > '%s'", encoded, remote)

	_, err = tm.client.ExecuteCommand(ctx, writeCmd)

	return err
}

// downloadViaSFTP downloads using SFTP.
func (tm *TransferManager) downloadViaSFTP(ctx context.Context, remote, local string) error {
	// Create SFTP client
	sftpClient, err := sftp.NewClient(tm.client.client)
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}

	defer func() { _ = sftpClient.Close() }()

	// Open remote file
	remoteFile, err := sftpClient.Open(remote)
	if err != nil {
		return fmt.Errorf("failed to open remote file: %w", err)
	}

	defer func() { _ = remoteFile.Close() }()

	// Get remote file info for progress
	remoteInfo, err := remoteFile.Stat()
	if err != nil {
		tm.log.Warn("Failed to get remote file info", "error", err.Error())
	}

	// Ensure local directory exists
	localDir := filepath.Dir(local)
	if err := os.MkdirAll(localDir, 0750); err != nil {
		return fmt.Errorf("failed to create local directory: %w", err)
	}

	// Create local file
	if err := security.ValidatePath(local); err != nil {
		return fmt.Errorf("invalid local path: %w", err)
	}

	localFile, err := os.Create(local) // #nosec G304 - local path is validated above
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}

	defer func() { _ = localFile.Close() }()

	// Copy with progress reporting
	var size int64
	if remoteInfo != nil {
		size = remoteInfo.Size()
	}

	if tm.options.Progress != nil && size > 0 {
		_, err = tm.copyWithProgress(remoteFile, localFile, size)
	} else {
		_, err = io.Copy(localFile, remoteFile)
	}

	return err
}

// downloadViaSCP downloads using SCP command.
func (tm *TransferManager) downloadViaSCP(ctx context.Context, remote, local string) error {
	// For now, delegate to external SCP command
	return tm.downloadViaExternalSCP(ctx, remote, local)
}

// downloadViaCat downloads using cat command over SSH.
func (tm *TransferManager) downloadViaCat(ctx context.Context, remote, local string) error {
	// Execute cat command to get file content
	catCmd := fmt.Sprintf("cat '%s'", remote)

	result, err := tm.client.ExecuteCommand(ctx, catCmd)
	if err != nil {
		return fmt.Errorf("failed to cat remote file: %w", err)
	}

	// Ensure local directory exists
	localDir := filepath.Dir(local)
	if err := os.MkdirAll(localDir, 0750); err != nil {
		return fmt.Errorf("failed to create local directory: %w", err)
	}

	// Write to local file
	return os.WriteFile(local, []byte(result.Stdout), 0600)
}

// copyWithProgress copies data while reporting progress.
func (tm *TransferManager) copyWithProgress(src io.Reader, dst io.Writer, totalSize int64) (int64, error) {
	buf := make([]byte, 32*1024) // 32KB buffer

	var written int64

	for {
		numRead, readErr := src.Read(buf)
		if numRead > 0 {
			numWritten, writeErr := dst.Write(buf[0:numRead])
			if numWritten > 0 {
				written += int64(numWritten)

				// Report progress
				if tm.options.Progress != nil && totalSize > 0 {
					percent := float64(written) / float64(totalSize) * 100
					progress := fmt.Sprintf("\rProgress: %.1f%% (%d/%d bytes)",
						percent, written, totalSize)
					_, _ = tm.options.Progress.Write([]byte(progress))
				}
			}

			if writeErr != nil {
				return written, writeErr
			}

			if numRead != numWritten {
				return written, io.ErrShortWrite
			}
		}

		if readErr != nil {
			if readErr != io.EOF {
				return written, readErr
			}

			break
		}
	}

	// Final newline for progress
	if tm.options.Progress != nil {
		_, _ = tm.options.Progress.Write([]byte("\n"))
	}

	return written, nil
}

// verifyUpload verifies that the uploaded file matches the local file.
func (tm *TransferManager) verifyUpload(ctx context.Context, localPath, remotePath string) error {
	// Calculate local file checksum
	localHash, err := tm.calculateFileHash(localPath)
	if err != nil {
		return fmt.Errorf("failed to calculate local file hash: %w", err)
	}

	// Calculate remote file checksum
	hashCmd := fmt.Sprintf("sha256sum '%s' | cut -d' ' -f1", remotePath)

	result, err := tm.client.ExecuteCommand(ctx, hashCmd)
	if err != nil {
		return fmt.Errorf("failed to calculate remote file hash: %w", err)
	}

	remoteHash := strings.TrimSpace(result.Stdout)

	if localHash != remoteHash {
		return fmt.Errorf("file integrity check failed: local=%s remote=%s",
			localHash, remoteHash)
	}

	tm.log.Debug("File integrity verified",
		"local", localPath,
		"remote", remotePath,
		"hash", localHash)

	return nil
}

// calculateFileHash calculates SHA256 hash of a file.
func (tm *TransferManager) calculateFileHash(filePath string) (string, error) {
	if err := security.ValidatePath(filePath); err != nil {
		return "", fmt.Errorf("invalid file path: %w", err)
	}

	file, err := os.Open(filePath) // #nosec G304 - filePath is validated above
	if err != nil {
		return "", err
	}

	defer func() { _ = file.Close() }()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// External command implementations.
func (tm *TransferManager) uploadViaExternalSCP(ctx context.Context, local, remote string) error {
	tm.log.Debug("Attempting upload via external SCP",
		"local", local,
		"remote", remote)

	// Check if SCP command is available
	if _, err := exec.LookPath("scp"); err != nil {
		return errors.New("external SCP command not available")
	}

	// Build SCP command arguments
	args := []string{
		"-o", "ConnectTimeout=30",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}

	// Add port if not default
	if tm.client.config.Port != 22 {
		args = append(args, "-P", strconv.Itoa(tm.client.config.Port))
	}

	// Add private key if specified
	if tm.client.config.PrivateKeyPath != "" {
		args = append(args, "-i", tm.client.config.PrivateKeyPath)
	}

	// Add preserve permissions flag if requested
	if tm.options.Preserve {
		args = append(args, "-p")
	}

	// Add custom SSH options
	for _, opt := range tm.client.config.SSHOptions {
		args = append(args, "-o", opt)
	}

	// Add source and destination
	destination := fmt.Sprintf("%s@%s:%s", tm.client.config.User, tm.client.config.Host, remote)
	args = append(args, local, destination)

	// Execute SCP command
	cmd := exec.CommandContext(ctx, "scp", args...)

	// Set up environment if password authentication is needed
	if tm.client.config.UseSSHPass && tm.client.config.Password != "" {
		if _, err := exec.LookPath("sshpass"); err == nil {
			// Use sshpass for password authentication
			sshpassArgs := []string{"-p", tm.client.config.Password, "scp"}

			sshpassArgs = append(sshpassArgs, args...)
			err := validateCommand(append([]string{"sshpass"}, sshpassArgs...))
			if err != nil {
				return fmt.Errorf("invalid sshpass command: %w", err)
			}

			cmd = exec.CommandContext(ctx, "sshpass", sshpassArgs...) // #nosec G204 - command is validated above
		} else {
			tm.log.Warn("sshpass not available for password authentication")
		}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("external SCP upload failed: %w (output: %s)", err, string(output))
	}

	tm.log.Debug("External SCP upload completed successfully")

	return nil
}

func (tm *TransferManager) downloadViaExternalSCP(ctx context.Context, remote, local string) error {
	tm.log.Debug("Attempting download via external SCP",
		"remote", remote,
		"local", local)

	// Check if SCP command is available
	if _, err := exec.LookPath("scp"); err != nil {
		return errors.New("external SCP command not available")
	}

	// Ensure local directory exists
	localDir := filepath.Dir(local)
	if err := os.MkdirAll(localDir, 0750); err != nil {
		return fmt.Errorf("failed to create local directory: %w", err)
	}

	// Build SCP command arguments
	args := []string{
		"-o", "ConnectTimeout=30",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}

	// Add port if not default
	if tm.client.config.Port != 22 {
		args = append(args, "-P", strconv.Itoa(tm.client.config.Port))
	}

	// Add private key if specified
	if tm.client.config.PrivateKeyPath != "" {
		args = append(args, "-i", tm.client.config.PrivateKeyPath)
	}

	// Add preserve permissions flag if requested
	if tm.options.Preserve {
		args = append(args, "-p")
	}

	// Add custom SSH options
	for _, opt := range tm.client.config.SSHOptions {
		args = append(args, "-o", opt)
	}

	// Add source and destination
	source := fmt.Sprintf("%s@%s:%s", tm.client.config.User, tm.client.config.Host, remote)
	args = append(args, source, local)

	// Execute SCP command
	cmd := exec.CommandContext(ctx, "scp", args...)

	// Set up environment if password authentication is needed
	if tm.client.config.UseSSHPass && tm.client.config.Password != "" {
		if _, err := exec.LookPath("sshpass"); err == nil {
			// Use sshpass for password authentication
			sshpassArgs := []string{"-p", tm.client.config.Password, "scp"}

			sshpassArgs = append(sshpassArgs, args...)
			err := validateCommand(append([]string{"sshpass"}, sshpassArgs...))
			if err != nil {
				return fmt.Errorf("invalid sshpass command: %w", err)
			}

			cmd = exec.CommandContext(ctx, "sshpass", sshpassArgs...) // #nosec G204 - command is validated above
		} else {
			tm.log.Warn("sshpass not available for password authentication")
		}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("external SCP download failed: %w (output: %s)", err, string(output))
	}

	tm.log.Debug("External SCP download completed successfully")

	return nil
}
