package main

import "crypto/sha256"

// digestOf returns the SHA-256 digest of data. It is the one place in this
// program that touches the hash package.
func digestOf(data []byte) [32]byte {
	return sha256.Sum256(data)
}
