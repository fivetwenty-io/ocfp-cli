package output

import "time"

// fixedTime is the deterministic timestamp shared across all output renderer tests.
// Using a fixed value eliminates scheduler jitter from every time.Now() call in test setup.
var fixedTime = time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
