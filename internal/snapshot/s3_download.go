package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/sync/errgroup"
)

type randomAccessArchive interface {
	io.Writer
	io.ReaderAt
	io.WriterAt
	io.Seeker
}

func (s *S3Store) downloadParallel(ctx context.Context, m Manifest, dst randomAccessArchive) error {
	base, err := dst.Seek(0, io.SeekCurrent)
	if err != nil {
		return s.downloadSequential(ctx, m, dst)
	}
	object, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(m.ID, ".tar.gz")),
	})
	// Preserve compatibility with providers that do not implement HEAD or
	// return an object identity suitable for conditional ranged reads.
	if err != nil || aws.ToString(object.ETag) == "" || object.ContentLength == nil {
		return s.downloadSequential(ctx, m, dst)
	}
	if aws.ToInt64(object.ContentLength) != m.SizeBytes {
		return ErrCorrupt
	}
	group, partCtx := errgroup.WithContext(ctx)
	group.SetLimit(uploadConcurrency)
	count := (m.SizeBytes + uploadPartSize - 1) / uploadPartSize
	for i := range count {
		group.Go(func() error {
			if err := partCtx.Err(); err != nil {
				return err
			}
			offset := i * uploadPartSize
			length := min(uploadPartSize, m.SizeBytes-offset)
			return s.downloadRange(partCtx, m, aws.ToString(object.ETag), dst, base, offset, length)
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	// Hash in archive order from disk after the concurrent writes finish.
	// No unverified bytes are ever passed to the rootfs extractor.
	sum := sha256.New()
	n, err := io.Copy(sum, ContextReader{Ctx: ctx, Reader: io.NewSectionReader(dst, base, m.SizeBytes)})
	if err != nil {
		return err
	}
	if n != m.SizeBytes || hex.EncodeToString(sum.Sum(nil)) != m.SHA256 {
		return ErrCorrupt
	}
	_, err = dst.Seek(base+m.SizeBytes, io.SeekStart)
	return err
}

func (s *S3Store) downloadRange(ctx context.Context, m Manifest, etag string, dst io.WriterAt, base, offset, length int64) error {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(m.ID, ".tar.gz")), IfMatch: aws.String(etag),
		Range: aws.String(fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)),
	})
	if err != nil {
		return storageError(err)
	}
	defer func() { _ = result.Body.Close() }()
	expectedRange := fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, m.SizeBytes)
	if aws.ToString(result.ContentRange) != expectedRange || aws.ToInt64(result.ContentLength) != length {
		return ErrCorrupt
	}
	// A server that ignores Range must never write across another worker's
	// section. Read one extra byte, but reject it before writing outside it.
	writer := &LimitedWriter{Writer: io.NewOffsetWriter(dst, base+offset), Remaining: length}
	n, err := io.Copy(writer, io.LimitReader(ContextReader{Ctx: ctx, Reader: result.Body}, length+1))
	if errors.Is(err, ErrTooLarge) || err == nil && n != length {
		return ErrCorrupt
	}
	return err
}
