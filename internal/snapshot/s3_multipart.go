package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/sync/errgroup"
)

const (
	uploadPartSize    int64 = 16 << 20
	uploadConcurrency       = 4
)

func (s *S3Store) uploadMultipart(ctx context.Context, id string, src io.ReaderAt, size int64) (err error) {
	key := s.key(id, ".tar.gz")
	created, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String("application/gzip"),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumType: types.ChecksumTypeComposite,
	})
	if err != nil {
		return err
	}
	if aws.ToString(created.UploadId) == "" {
		return fmt.Errorf("multipart upload returned an empty upload ID")
	}
	defer func() {
		if err == nil {
			return
		}
		// Wait for every worker before aborting. Use a fresh deadline so a
		// canceled snapshot still removes its unfinished parts from the bucket.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, cleanupErr := s.client.AbortMultipartUpload(cleanupCtx, &s3.AbortMultipartUploadInput{
			Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: created.UploadId,
		})
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("abort multipart upload: %w", cleanupErr))
		}
	}()

	parts, err := s.uploadParts(ctx, key, aws.ToString(created.UploadId), src, size)
	if err != nil {
		return err
	}
	_, err = s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: created.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
		ChecksumType:    types.ChecksumTypeComposite,
	})
	return err
}

func (s *S3Store) uploadParts(ctx context.Context, key, uploadID string, src io.ReaderAt, size int64) ([]types.CompletedPart, error) {
	parts := make([]types.CompletedPart, (size+uploadPartSize-1)/uploadPartSize)
	group, partCtx := errgroup.WithContext(ctx)
	group.SetLimit(uploadConcurrency)
	for i := range parts {
		group.Go(func() error {
			if err := partCtx.Err(); err != nil {
				return err
			}
			offset := int64(i) * uploadPartSize
			length := min(uploadPartSize, size-offset)
			// Upload validates a maximum archive size of 4 GiB, so there are
			// at most 256 parts, well within S3's int32 part number range.
			part, err := s.uploadPart(partCtx, key, uploadID, int32(i+1), io.NewSectionReader(src, offset, length), length) // #nosec G115 -- bounded by Upload's size check.
			if err != nil {
				return fmt.Errorf("upload snapshot part %d: %w", i+1, err)
			}
			// Each worker owns its element; completion order does not affect
			// the ascending part order required by CompleteMultipartUpload.
			parts[i] = part
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return parts, nil
}

func (s *S3Store) uploadPart(ctx context.Context, key, uploadID string, number int32, src *io.SectionReader, size int64) (types.CompletedPart, error) {
	sum := sha256.New()
	n, err := io.Copy(sum, ContextReader{Ctx: ctx, Reader: src})
	if err != nil {
		return types.CompletedPart{}, err
	}
	if n != size {
		return types.CompletedPart{}, ErrCorrupt
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return types.CompletedPart{}, err
	}
	checksum := base64.StdEncoding.EncodeToString(sum.Sum(nil))
	result, err := s.client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		PartNumber: aws.Int32(number), Body: src, ContentLength: aws.Int64(size),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: aws.String(checksum),
	})
	if err != nil {
		return types.CompletedPart{}, err
	}
	if aws.ToString(result.ETag) == "" || result.ChecksumSHA256 != nil && aws.ToString(result.ChecksumSHA256) != checksum {
		return types.CompletedPart{}, ErrCorrupt
	}
	return types.CompletedPart{PartNumber: aws.Int32(number), ETag: result.ETag, ChecksumSHA256: aws.String(checksum)}, nil
}
