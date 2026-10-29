// Package image manages base root filesystems: directories under the images
// dir whose ownership is shifted into the sandbox user-namespace id range, so
// files owned by root inside the image are owned by sandbox root, not host root.
package image

import (
	"errors"
	"regexp"
	"time"
)

var (
	ErrNotFound    = errors.New("image not found")
	ErrExists      = errors.New("image already exists")
	ErrInvalidName = errors.New("image name must match [a-z0-9][a-z0-9._-]{0,63}")
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidName reports whether name is a safe image directory name.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

type Image struct {
	Name      string
	CreatedAt time.Time
}
