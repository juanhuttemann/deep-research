//go:build !unix

package ui

import (
	"errors"
	"os"
)

// newTTYInput is unavailable on non-Unix platforms.
func newTTYInput(f *os.File) (Input, error) {
	return nil, errors.New("raw terminal input is only supported on unix")
}
