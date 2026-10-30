package snapshot

import (
	"context"
	"crypto/x509"
	"errors"
	"net"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// ConnectionTestMessage translates provider failures into actionable, safe text.
// Never return provider messages, arbitrary codes, URLs or wrapped errors: they
// may echo credentials or other request data.
func ConnectionTestMessage(err error) string {
	stage := "cloud storage test failed"
	var operation *smithy.OperationError
	if errors.As(err, &operation) {
		switch operation.OperationName {
		case "ListObjectsV2":
			stage = "cloud storage test failed while listing backups (s3:ListBucket)"
		case "PutObject":
			stage = "cloud storage test failed while uploading a test object (s3:PutObject)"
		case "GetObject":
			stage = "cloud storage test failed while reading a test object (s3:GetObject)"
		case "DeleteObject":
			stage = "cloud storage test failed while deleting a test object (s3:DeleteObject)"
		}
	}
	return stage + ": " + connectionTestHint(err)
}

func connectionTestHint(err error) string {
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "AccessDenied", "AllAccessDisabled":
			return "access denied; check the identity's IAM policy, bucket policy, backup prefix and encryption-key permissions"
		case "InvalidAccessKeyId":
			return "access key ID was rejected; check that the key is correct and active"
		case "SignatureDoesNotMatch", "InvalidSignatureException":
			return "request signature was rejected; check the secret key, region and endpoint"
		case "ExpiredToken", "ExpiredTokenException", "TokenRefreshRequired":
			return "temporary credentials expired; replace the access key, secret key and session token together"
		case "InvalidToken", "InvalidClientTokenId":
			return "security token was rejected; check the credentials and session token"
		case "NoSuchBucket":
			return "bucket was not found; enter an existing bucket name without s3:// or a URL"
		case "AuthorizationHeaderMalformed", "PermanentRedirect", "IncorrectEndpoint", "IllegalLocationConstraintException":
			return "check that the region matches the bucket; leave the endpoint empty for AWS S3"
		case "RequestTimeTooSkewed", "RequestExpired":
			return "request time was rejected; synchronize the daemon host's clock"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "connection timed out; check the endpoint and the daemon's network access"
	}
	var certificateError x509.UnknownAuthorityError
	if errors.As(err, &certificateError) {
		return "TLS certificate is not trusted; check the endpoint and the daemon's trusted certificates"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return "network connection failed; check DNS, the endpoint and the daemon's network access"
	}
	var responseError *smithyhttp.ResponseError
	if errors.As(err, &responseError) && responseError.HTTPStatusCode() == 403 {
		return "access denied; check credentials, bucket permissions and the backup prefix"
	}
	if errors.Is(err, ErrCorrupt) {
		return "the test object's contents did not match; check the storage endpoint"
	}
	return "check the bucket, region, endpoint, credentials and list/read/write/delete permissions; server identity requires credentials available to the daemon"
}
