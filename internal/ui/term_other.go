//go:build !unix

package ui

import "os"

// rawTermSize is unavailable on non-Unix platforms.
//
// Reporting (0, 0) means IsTTYFile is false everywhere here, so a real Windows
// console is treated as a pipe: the renderer runs in log mode (one line per
// event, no live frame) and the interactive keys are inert. That is a genuine
// downgrade, not a fallback to an 80x24 frame, and it is what an earlier
// comment here claimed did not happen.
func rawTermSize(f *os.File) (rows, cols int) { return 0, 0 }
