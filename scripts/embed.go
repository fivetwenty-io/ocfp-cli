// Package scripts embeds the provisioning scripts into the ocfp binary so an
// installed binary works from any directory, without a copy of the source
// tree. On-disk copies (repo checkout, /opt/ocfp) still take precedence —
// see commands.FindProvisionScript.
package scripts

import "embed"

// Provision holds the scripts under scripts/provision/ (bastion, artifacts).
//
//go:embed provision/*
var Provision embed.FS
