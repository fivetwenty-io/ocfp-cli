// Package enroll holds the open formats and checks by which a bastion's
// enrollment service asks a managing system to admit its bloc worker,
// and then fetches and verifies the worker.
//
// The enrollment address tells the service where the managing system's
// bus is and which subject roots to use, so nothing here names the
// managing system. The bootstrap signs an Attestation with the bastion's
// host key, and the service sends it as a Request through an admission
// account whose users it mints for itself. A person compares the host
// key's fingerprint and confirmation code with their terminal and
// approves. The service then reads the worker's manifest set and its
// chunks, checks the set through the sigchain package and the version
// floors, and checks every chunk against the manifest.
package enroll
