package snapshot

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
)

func TestConnectionTestMessageDiagnosesProviderErrorsWithoutReflectingInput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code, hint string
	}{
		{"AccessDenied", "access denied"},
		{"InvalidAccessKeyId", "key ID was rejected"},
		{"SignatureDoesNotMatch", "signature was rejected"},
		{"ExpiredToken", "credentials expired"},
		{"InvalidToken", "token was rejected"},
		{"NoSuchBucket", "bucket was not found"},
		{"AuthorizationHeaderMalformed", "region matches the bucket"},
		{"PermanentRedirect", "region matches the bucket"},
		{"RequestTimeTooSkewed", "host's clock"},
		{"untrusted-secret-code", "check the bucket"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			err := &smithy.OperationError{ServiceID: "S3", OperationName: "ListObjectsV2", Err: &smithy.GenericAPIError{
				Code: tc.code, Message: "untrusted-secret-message https://secret.example/",
			}}
			message := ConnectionTestMessage(fmt.Errorf("untrusted-secret-wrapper: %w", err))
			if !strings.Contains(message, "s3:ListBucket") || !strings.Contains(message, tc.hint) {
				t.Fatalf("missing diagnosis: %s", message)
			}
			if strings.Contains(message, "untrusted-secret") || strings.Contains(message, "secret.example") {
				t.Fatalf("reflected provider input: %s", message)
			}
		})
	}
}

func TestConnectionTestMessageOperationsAndTransport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		hint string
	}{
		{"upload", &smithy.OperationError{OperationName: "PutObject", Err: errors.New("private")}, "s3:PutObject"},
		{"read", &smithy.OperationError{OperationName: "GetObject", Err: errors.New("private")}, "s3:GetObject"},
		{"delete", &smithy.OperationError{OperationName: "DeleteObject", Err: errors.New("private")}, "s3:DeleteObject"},
		{"unknown operation", &smithy.OperationError{OperationName: "private", Err: errors.New("private")}, "cloud storage test failed:"},
		{"timeout", context.DeadlineExceeded, "timed out"},
		{"dns", &net.DNSError{Err: "private", Name: "private"}, "network connection failed"},
		{"tls", x509.UnknownAuthorityError{}, "TLS certificate"},
		{"corrupt", ErrCorrupt, "contents did not match"},
		{"cleanup", errors.Join(nil, &smithy.OperationError{OperationName: "DeleteObject", Err: errors.New("private")}), "s3:DeleteObject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			message := ConnectionTestMessage(tc.err)
			if !strings.Contains(message, tc.hint) || strings.Contains(message, "private") {
				t.Fatalf("unsafe or incorrect diagnosis: %s", message)
			}
		})
	}
}

// Transfer acceleration is an AWS S3 service reached at its own host, so the
// settings that point somewhere else, or address a bucket by path, cannot
// come with it.
func TestAccelerationRefusesSettingsItCannotHonour(t *testing.T) {
	base := Connection{AuthMode: AuthServerIdentity, Bucket: "backups", Region: "ap-southeast-2", Accelerate: true}
	if err := base.Validate(); err != nil {
		t.Fatalf("a plain accelerated connection was rejected: %v", err)
	}
	for name, broken := range map[string]Connection{
		"custom endpoint": {AuthMode: AuthServerIdentity, Bucket: "backups", Region: "ap-southeast-2", Accelerate: true, Endpoint: "https://minio.example"},
		"path style":      {AuthMode: AuthServerIdentity, Bucket: "backups", Region: "ap-southeast-2", Accelerate: true, PathStyle: true},
		"dotted bucket":   {AuthMode: AuthServerIdentity, Bucket: "my.backups", Region: "ap-southeast-2", Accelerate: true},
	} {
		if err := broken.Validate(); !errors.Is(err, ErrConnectionInvalid) {
			t.Errorf("%s was accepted alongside acceleration: %v", name, err)
		}
	}
	// The same settings are fine when acceleration is off.
	off := Connection{AuthMode: AuthServerIdentity, Bucket: "my.backups", Region: "us-east-1", Endpoint: "https://minio.example", PathStyle: true}
	if err := off.Validate(); err != nil {
		t.Fatalf("acceleration rules leaked into an ordinary connection: %v", err)
	}
}
