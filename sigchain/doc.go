// Package sigchain checks a payload against a chain of keys that an
// offline root anchors.
//
// The offline root signs a short-lived online key's statement, and the
// online key signs a payload, such as a bloc worker's manifest or the
// checksum file of a CLI release. The root also signs one revocation
// statement for every online key it has signed, whose sequence rises by
// one with each statement. Verify runs the five steps of the chain in a
// fixed order and refuses with a coded Error at the first step that
// fails. Every signature is an SSHSIG, as ssh-keygen -Y sign makes it.
//
// The package embeds up to two root public keys and the newest
// revocation statement at build time, and Embedded returns them.
package sigchain
