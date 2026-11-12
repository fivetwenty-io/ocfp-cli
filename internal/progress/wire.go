// Package progress writes and reads the progress stream that a long-running
// ocfp command produces, one JSON object per line.
package progress

// WireContract is the version of the progress stream format and of the job
// request that asks for it. A reader compares the header's wireContract with
// this value before it trusts the rest of the stream. Contract 2 added the org
// and the bloc to the job request.
const WireContract = 2
