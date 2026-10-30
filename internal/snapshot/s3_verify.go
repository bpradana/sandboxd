package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Verify compares S3's validated SHA-256 against the local archive. Multipart
// SHA-256 is a composite of part hashes, never the full-file manifest digest.
// Providers that cannot return checksums retain the full-download check.
func (s *S3Store) Verify(ctx context.Context, m Manifest, src io.ReaderAt) error {
	if !ValidID(m.ID) {
		return ErrInvalidID
	}
	if m.SizeBytes <= 0 || m.SizeBytes > s.maxBytes {
		return ErrTooLarge
	}
	sum, err := hex.DecodeString(m.SHA256)
	if err != nil || len(sum) != sha256.Size {
		return ErrCorrupt
	}
	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(m.ID, ".tar.gz")), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil || aws.ToString(result.ChecksumSHA256) == "" {
		return s.Download(ctx, m, io.Discard)
	}
	if aws.ToInt64(result.ContentLength) != m.SizeBytes {
		return ErrCorrupt
	}
	expected := base64.StdEncoding.EncodeToString(sum)
	if result.ChecksumType == types.ChecksumTypeComposite {
		expected, err = compositeChecksum(ctx, src, m)
		if err != nil {
			return err
		}
	}
	if aws.ToString(result.ChecksumSHA256) != expected {
		return ErrCorrupt
	}
	return nil
}

func compositeChecksum(ctx context.Context, src io.ReaderAt, m Manifest) (string, error) {
	full, composite := sha256.New(), sha256.New()
	count := (m.SizeBytes + uploadPartSize - 1) / uploadPartSize
	for i := range count {
		part := sha256.New()
		length := min(uploadPartSize, m.SizeBytes-i*uploadPartSize)
		n, err := io.Copy(io.MultiWriter(full, part), ContextReader{Ctx: ctx, Reader: io.NewSectionReader(src, i*uploadPartSize, length)})
		if err != nil {
			return "", err
		}
		if n != length {
			return "", ErrCorrupt
		}
		_, _ = composite.Write(part.Sum(nil))
	}
	if hex.EncodeToString(full.Sum(nil)) != m.SHA256 {
		return "", ErrCorrupt
	}
	return fmt.Sprintf("%s-%d", base64.StdEncoding.EncodeToString(composite.Sum(nil)), count), nil
}
