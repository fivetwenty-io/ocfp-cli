package state

import "time"

// nowFn is the clock used throughout the state package. Tests override it
// to a fixed time so assertions can use exact equality instead of range checks.
var nowFn = time.Now
