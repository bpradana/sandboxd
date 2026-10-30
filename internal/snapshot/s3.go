package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/helper"
)

type S3Store struct {
	client         *s3.Client
	bucket, prefix string
	maxBytes       int64
}

func NewS3Store(ctx context.Context, cfg config.SnapshotConfig) (Store, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	return NewConnectionStore(ctx, cfg, ConnectionFromConfig(cfg))
}

func NewConnectionStore(ctx context.Context, cfg config.SnapshotConfig, connection Connection) (ConnectionStore, error) {
	if err := connection.Validate(); err != nil {
		return nil, err
	}
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(connection.Region), awsconfig.WithHTTPClient(&http.Client{Timeout: cfg.Timeout})}
	if connection.AuthMode == AuthAccessKeys {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(connection.AccessKeyID, connection.SecretAccessKey, connection.SessionToken)))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = connection.PathStyle
		// Let the SDK build and sign the accelerate endpoint; it is not the
		// regional host with a different name.
		o.UseAccelerate = connection.Accelerate
		if connection.Endpoint != "" {
			o.BaseEndpoint = aws.String(connection.Endpoint)
		}
	})
	return &S3Store{client: client, bucket: connection.Bucket, prefix: strings.Trim(connection.Prefix, "/"), maxBytes: cfg.MaxBytes}, nil
}

func (s *S3Store) TestConnection(ctx context.Context) (err error) {
	prefix := s.prefix
	if prefix != "" {
		prefix += "/"
	}
	if _, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(1)}); err != nil {
		return err
	}
	key := s.key(helper.NewID("connection-test"), ".probe")
	probe := []byte(helper.NewID("probe"))
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, cleanupErr := s.client.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
		err = errors.Join(err, cleanupErr)
	}()
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(probe)}); err != nil {
		return err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	defer func() { _ = result.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(result.Body, int64(len(probe))+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(data, probe) {
		return ErrCorrupt
	}
	return nil
}

func (s *S3Store) key(id, suffix string) string {
	if s.prefix == "" {
		return id + suffix
	}
	return s.prefix + "/" + id + suffix
}

func (s *S3Store) PutManifest(ctx context.Context, m Manifest) error {
	if !ValidID(m.ID) {
		return ErrInvalidID
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(m.ID, ".json")), Body: bytes.NewReader(data), ContentType: aws.String("application/json")})
	return err
}

func (s *S3Store) GetManifest(ctx context.Context, id string) (Manifest, error) {
	var m Manifest
	if !ValidID(id) {
		return m, ErrInvalidID
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(id, ".json"))})
	if err != nil {
		return m, storageError(err)
	}
	defer func() { _ = result.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(result.Body, (1<<20)+1))
	if err != nil {
		return m, err
	}
	if len(data) > 1<<20 {
		return m, ErrTooLarge
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.ID != id || m.Version != Version {
		return m, fmt.Errorf("unsupported or invalid snapshot manifest")
	}
	return m, nil
}

func (s *S3Store) List(ctx context.Context) ([]Manifest, error) {
	items := make([]Manifest, 0)
	prefix := s.prefix
	if prefix != "" {
		prefix += "/"
	}
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Contents {
			name := strings.TrimPrefix(aws.ToString(object.Key), prefix)
			id, ok := strings.CutSuffix(name, ".json")
			if !ok || !ValidID(id) {
				continue
			}
			m, err := s.GetManifest(ctx, id)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			items = append(items, m)
		}
	}
	return items, nil
}

func (s *S3Store) Upload(ctx context.Context, id string, src io.ReadSeeker, size int64, digest string) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	if size <= 0 || size > s.maxBytes {
		return ErrTooLarge
	}
	sum, err := hex.DecodeString(digest)
	if err != nil || len(sum) != sha256.Size {
		return ErrCorrupt
	}
	// Snapshot archives are files: independent sections let workers read and
	// retry parts without buffering the whole archive or sharing a seek cursor.
	if reader, ok := src.(io.ReaderAt); ok && size > uploadPartSize {
		return s.uploadMultipart(ctx, id, reader, size)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(id, ".tar.gz")),
		Body: src, ContentLength: aws.Int64(size), ContentType: aws.String("application/gzip"),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum)),
	})
	if err != nil {
		return err
	}
	// A single request either stored the whole archive or did not store it.
	newTransfer(ctx).set(0, size)
	return nil
}

func (s *S3Store) Download(ctx context.Context, m Manifest, dst io.Writer) error {
	if !ValidID(m.ID) {
		return ErrInvalidID
	}
	if m.SizeBytes <= 0 || m.SizeBytes > s.maxBytes {
		return ErrTooLarge
	}
	if file, ok := dst.(randomAccessArchive); ok && m.SizeBytes > uploadPartSize {
		return s.downloadParallel(ctx, m, file)
	}
	return s.downloadSequential(ctx, m, dst)
}

func (s *S3Store) downloadSequential(ctx context.Context, m Manifest, dst io.Writer) error {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(m.ID, ".tar.gz"))})
	if err != nil {
		return storageError(err)
	}
	defer func() { _ = result.Body.Close() }()
	sum := sha256.New()
	received := newTransfer(ctx)
	n, err := io.Copy(io.MultiWriter(received.writer(0, dst), sum), io.LimitReader(ContextReader{Ctx: ctx, Reader: result.Body}, m.SizeBytes+1))
	if err != nil {
		return err
	}
	if n != m.SizeBytes || hex.EncodeToString(sum.Sum(nil)) != m.SHA256 {
		return ErrCorrupt
	}
	return nil
}

func (s *S3Store) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	// Remove the archive first, retaining the manifest if deletion fails so a
	// retry can still discover and clean up the snapshot.
	for _, suffix := range []string{".tar.gz", ".json"} {
		if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(id, suffix))}); err != nil {
			return err
		}
	}
	return nil
}

func storageError(err error) error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound") {
		return ErrNotFound
	}
	return err
}
