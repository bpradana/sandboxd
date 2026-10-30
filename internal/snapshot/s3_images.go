package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// imagePrefix keeps image references out of the snapshot listing: a manifest
// key is "<id>.json" and an image key is "images/<name>.json", so the two
// never collide and List skips what is not a snapshot id.
const imagePrefix = "images/"

// ErrInvalidImageName rejects a name that could escape the prefix or collide
// with a snapshot key. It is the same shape the image store accepts.
var ErrInvalidImageName = errors.New("invalid image name")

var imageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func ValidImageName(name string) bool { return imageNamePattern.MatchString(name) }

func (s *S3Store) imageKey(name string) string {
	return s.key(imagePrefix+name, ".json")
}

func (s *S3Store) PutImageRef(ctx context.Context, ref ImageRef) error {
	if !ValidImageName(ref.Name) {
		return ErrInvalidImageName
	}
	data, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.imageKey(ref.Name)),
		Body: bytes.NewReader(data), ContentType: aws.String("application/json"),
	})
	return err
}

func (s *S3Store) GetImageRef(ctx context.Context, name string) (ImageRef, error) {
	var ref ImageRef
	if !ValidImageName(name) {
		return ref, ErrInvalidImageName
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.imageKey(name)),
	})
	if err != nil {
		return ref, storageError(err)
	}
	defer func() { _ = result.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(result.Body, (1<<20)+1))
	if err != nil {
		return ref, err
	}
	if len(data) > 1<<20 {
		return ref, ErrTooLarge
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		return ref, err
	}
	if ref.Name != name || !ValidID(ref.SnapshotID) {
		return ref, ErrNotFound
	}
	return ref, nil
}

func (s *S3Store) ListImageRefs(ctx context.Context) ([]ImageRef, error) {
	refs := make([]ImageRef, 0)
	prefix := s.prefix
	if prefix != "" {
		prefix += "/"
	}
	prefix += imagePrefix
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket), Prefix: aws.String(prefix),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Contents {
			name, ok := strings.CutSuffix(strings.TrimPrefix(aws.ToString(object.Key), prefix), ".json")
			if !ok || !ValidImageName(name) {
				continue
			}
			ref, err := s.GetImageRef(ctx, name)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func (s *S3Store) DeleteImageRef(ctx context.Context, name string) error {
	if !ValidImageName(name) {
		return ErrInvalidImageName
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.imageKey(name)),
	})
	return err
}
