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
