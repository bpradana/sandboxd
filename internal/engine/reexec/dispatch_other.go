//go:build !linux

package reexec

// Dispatch never matches off Linux: the sandbox engine is Linux-only.
func Dispatch() bool {
	return false
}
