package commands

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"syscall"

	pveapierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
	"github.com/ocfp/ocfp-cli-go/internal/blocinv"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/aws"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/azure"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/gcp"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
)

// The text of a config or provider error can carry what the error is about:
// a validation error quotes the config value it rejects, and a provider error
// can carry the provider's response body. The live section therefore reports
// a fixed class for each failure and only the fields known to be safe, never
// the error's own text.

// classifyLiveConfigError classifies a failure to load the bloc's config from
// configFile. A failure that is not a parse, file, or timeout error, and not a
// missing bloc, came from the loader's defaults and validation, so it is an
// invalid config.
func classifyLiveConfigError(err error, configFile string) error {
	if err == nil {
		return nil
	}

	classified := blocinv.Classify(err, configFile)

	switch {
	case classified.Kind != blocinv.KindUnexpected:
		return classified
	case errors.Is(err, config.ErrBlocNotDefined):
		classified.Kind = blocinv.KindBlocNotInConfig
	default:
		classified.Kind = blocinv.KindConfigInvalid
	}

	return classified
}

// classifyProviderError classifies a failed provider call. vmID names the VM
// the call was about, or is empty for a call about no single VM. An error that
// fits no known class gets fallback.
func classifyProviderError(err error, fallback blocinv.Kind, vmID string) error {
	if err == nil {
		return nil
	}

	return &blocinv.ClassifiedError{
		Kind:       providerErrorKind(err, fallback),
		VMID:       vmID,
		HTTPStatus: providerHTTPStatus(err),
		Err:        err,
	}
}

// providerErrorKind picks the class of a provider error. A timeout is checked
// first because a connection error can wrap one.
func providerErrorKind(err error, fallback blocinv.Kind) blocinv.Kind {
	switch {
	case isTimeoutError(err):
		return blocinv.KindTimeout
	case errors.Is(err, context.Canceled):
		return blocinv.KindCanceled
	case isInstanceNotFound(err):
		return blocinv.KindVMNotFound
	case isPermissionDenied(err):
		return blocinv.KindPermissionDenied
	case isAuthFailure(err):
		return blocinv.KindProviderAuth
	case isConnectionFailure(err):
		return blocinv.KindProviderConnect
	default:
		return fallback
	}
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || pveapierrors.IsTimeoutError(err) {
		return true
	}

	var netErr net.Error

	return errors.As(err, &netErr) && netErr.Timeout()
}

// isInstanceNotFound reports whether a provider says the VM does not exist.
// Only errors each provider classifies as not found count. Anything else,
// such as a refused permission or a server error, says nothing about whether
// the VM exists.
func isInstanceNotFound(err error) bool {
	return errors.Is(err, pve.ErrVMNotFound) ||
		errors.Is(err, pveapierrors.ErrNotFound) ||
		cpi.IsNotFound(err) ||
		aws.IsNotFound(err) ||
		gcp.IsNotFound(err) ||
		azure.IsNotFound(err)
}

func isPermissionDenied(err error) bool {
	if errors.Is(err, pveapierrors.ErrForbidden) || errors.Is(err, fs.ErrPermission) {
		return true
	}

	var perr *cpi.ProviderError

	return errors.As(err, &perr) && (perr.Code == "Forbidden" || perr.Code == "403")
}

func isAuthFailure(err error) bool {
	if errors.Is(err, pveapierrors.ErrUnauthorized) ||
		errors.Is(err, pve.ErrAuthenticationFailed) ||
		errors.Is(err, pve.ErrInvalidCredentials) ||
		errors.Is(err, pve.ErrTicketExpired) ||
		pveapierrors.IsTFARequired(err) {
		return true
	}

	var perr *cpi.ProviderError

	return errors.As(err, &perr) && (perr.Code == "Unauthorized" || perr.Code == "401")
}

func isConnectionFailure(err error) bool {
	if pveapierrors.IsConnectionError(err) || pveapierrors.IsSSLError(err) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}

	var (
		opErr  *net.OpError
		dnsErr *net.DNSError
	)

	return errors.As(err, &opErr) || errors.As(err, &dnsErr)
}

// providerHTTPStatus returns the HTTP status code a PVE API error carries, or
// 0 when the error carries none.
func providerHTTPStatus(err error) int {
	var (
		apiErr   *pveapierrors.APIError
		permErr  *pveapierrors.PermissionError
		paramErr *pveapierrors.ParameterError
		authErr  *pveapierrors.AuthenticationError
	)

	switch {
	case errors.As(err, &permErr):
		return permErr.HTTPCode
	case errors.As(err, &paramErr):
		return paramErr.HTTPCode
	case errors.As(err, &authErr):
		return authErr.HTTPCode
	case errors.As(err, &apiErr):
		return apiErr.HTTPCode
	default:
		return 0
	}
}
