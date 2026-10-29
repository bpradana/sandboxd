//go:build linux

package reexec

import (
	"os"
	"path/filepath"
)

// Dispatch runs the registered entrypoint when the process was re-executed
// under one of the marker names, and reports whether it did. Entrypoints never
// return; they exit the process themselves.
func Dispatch() bool {
	fn, ok := handlers[filepath.Base(os.Args[0])]
	if !ok {
		return false
	}
	fn()
	return true
}
