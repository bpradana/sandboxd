package snapshot

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bpradana/sandboxd/internal/config"
)

const (
	AuthAccessKeys     = "access_keys"
	AuthServerIdentity = "server_identity"
)

var ErrConnectionInvalid = errors.New("invalid cloud storage connection")

// Connection contains daemon-only credentials. Never serialize it in an API
// response or include it in logs, spans, sandbox specs, or snapshot manifests.
type Connection struct {
	AuthMode        string `json:"auth_mode"`
	Bucket          string `json:"bucket"`
	Region          string `json:"region"`
	Endpoint        string `json:"endpoint"`
	Prefix          string `json:"prefix"`
	PathStyle       bool   `json:"path_style"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
}

func (c Connection) Validate() error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrConnectionInvalid, message) }
	if c.AuthMode != AuthAccessKeys && c.AuthMode != AuthServerIdentity {
		return invalid("choose access keys or server identity")
	}
	if c.Bucket == "" || len(c.Bucket) > 255 || strings.ContainsAny(c.Bucket, " /\\\t\r\n") {
		return invalid("provide a bucket name without slashes or spaces")
	}
	if strings.TrimSpace(c.Region) == "" || len(c.Region) > 64 {
		return invalid("provide a region")
	}
	if len(c.Prefix) > 1024 {
		return invalid("prefix must be at most 1024 characters")
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(c.Endpoint) > 2048 {
			return invalid("endpoint must be an http(s) URL without credentials, query, or fragment")
		}
	}
	if c.AuthMode == AuthAccessKeys {
		if strings.TrimSpace(c.AccessKeyID) == "" || strings.TrimSpace(c.SecretAccessKey) == "" || len(c.AccessKeyID) > 256 || len(c.SecretAccessKey) > 4096 || len(c.SessionToken) > 16384 {
			return invalid("provide both an access key and a secret key")
		}
	} else if c.AccessKeyID != "" || c.SecretAccessKey != "" || c.SessionToken != "" {
		return invalid("server identity cannot include access keys or a session token")
	}
	return nil
}

func ConnectionFromConfig(cfg config.SnapshotConfig) Connection {
	return Connection{AuthMode: AuthServerIdentity, Bucket: cfg.Bucket, Region: cfg.Region, Endpoint: cfg.Endpoint, Prefix: cfg.Prefix, PathStyle: cfg.PathStyle}
}

type ConnectionRecord struct {
	Disconnected bool       `json:"disconnected"`
	Connection   Connection `json:"connection"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ConnectionStore supports a real permissions test without changing the live
// backup destination. Probe objects contain random bytes, never credentials.
type ConnectionStore interface {
	Store
	TestConnection(context.Context) error
}
