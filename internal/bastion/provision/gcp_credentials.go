package provision

// GenerateGCPCredentialsScript materializes transported credentials without
// interpolating their JSON into shell syntax or a world-readable profile.
// The caller sends OCFP_GCP_CREDENTIALS_B64 in its redacted environment.
func GenerateGCPCredentialsScript() string {
	return `# OCFP GCP credentials
if [ "${OCFP_PROVIDER:-}" = gcp ] && [ -n "${OCFP_GCP_CREDENTIALS_B64:-}" ]; then
    (
        umask 077
        case "${OCFP_BLOC:-}" in ""|.|..|*/*) echo 'Invalid bloc for GCP credentials' >&2; exit 1;; esac
        credential_dir="$HOME/.config/ocfp/$OCFP_BLOC"
        for credential_parent in "$HOME/.config" "$HOME/.config/ocfp" "$credential_dir"; do
            [ ! -L "$credential_parent" ] || { echo 'Refusing linked credential directory' >&2; exit 1; }
            mkdir -p "$credential_parent" || exit 1
        done
        chmod 700 "$credential_dir" || exit 1
        credential_file="$credential_dir/gcp-credentials.json"
        [ ! -L "$credential_file" ] || { echo 'Refusing linked credential file' >&2; exit 1; }
        credential_tmp=$(mktemp "$credential_dir/.gcp-credentials.XXXXXX") || exit 1
        if ! printf '%s' "$OCFP_GCP_CREDENTIALS_B64" | base64 --decode > "$credential_tmp"; then
            rm -f "$credential_tmp"
            echo 'Cannot decode GCP credentials' >&2
            exit 1
        fi
        chmod 600 "$credential_tmp" && mv -f "$credential_tmp" "$credential_file" || { rm -f "$credential_tmp"; exit 1; }
    ) || exit 1
    export GOOGLE_APPLICATION_CREDENTIALS="$HOME/.config/ocfp/$OCFP_BLOC/gcp-credentials.json"
    unset OCFP_GCP_CREDENTIALS_B64
fi
# End OCFP GCP credentials
`
}

// #nosec G101 -- this shell profile contains only a private file path, not credential material.
const gcpCredentialProfile = `# Use this user's private GCP credential file, never inline JSON.
if [ "${OCFP_PROVIDER:-}" = gcp ] && [ -r "$HOME/.config/ocfp/$OCFP_BLOC/gcp-credentials.json" ]; then
    export GOOGLE_APPLICATION_CREDENTIALS="$HOME/.config/ocfp/$OCFP_BLOC/gcp-credentials.json"
fi`
