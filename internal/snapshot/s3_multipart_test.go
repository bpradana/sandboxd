package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bpradana/sandboxd/internal/config"
)

const multipartTestID = "snap-0123456789abcdef01234567"

func multipartTestStore(t *testing.T, handler http.Handler) *S3Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, err := NewConnectionStore(context.Background(), config.SnapshotConfig{MaxBytes: 4 << 30, Timeout: time.Minute}, Connection{
		AuthMode: AuthAccessKeys, AccessKeyID: "multipart-test", SecretAccessKey: "multipart-test-secret", // pragma: allowlist secret -- httptest credential.
		Bucket: "backups", Region: "us-east-1", Endpoint: server.URL, PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	s3store, ok := store.(*S3Store)
	if !ok {
		t.Fatal("unexpected store type")
	}
	options := s3store.client.Options()
	options.Retryer = retry.NewStandard(func(o *retry.StandardOptions) { o.MaxBackoff = time.Millisecond })
	s3store.client = s3.New(options)
	return s3store
}

func TestMultipartSnapshotParallelRetryAndRoundTrip(t *testing.T) {
	// Four differently sized file sections must all be in flight before any
	// succeeds. The second part fails once to exercise the SDK's part retry.
	data := bytes.Repeat([]byte("snapshot-data-"), int(3*uploadPartSize/14)+1)
	data = append(data, []byte("tail")...)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	file, err := os.CreateTemp(t.TempDir(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	size := int64(len(data))
	var mu sync.Mutex
	parts := make(map[int][]byte)
	attempts := make(map[int]int)
	active, peak := 0, 0
	allStarted := make(chan struct{})
	completed := false
	reads := 0
	store := multipartTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			if r.Header.Get("X-Amz-Checksum-Algorithm") != "SHA256" {
				t.Error("missing multipart checksum algorithm")
			}
			_, _ = io.WriteString(w, "<InitiateMultipartUploadResult><UploadId>test-upload</UploadId></InitiateMultipartUploadResult>")
		case r.Method == http.MethodPut && r.URL.Query().Get("uploadId") == "test-upload":
			number, _ := strconv.Atoi(r.URL.Query().Get("partNumber"))
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sum := sha256.Sum256(body)
			checksum := base64.StdEncoding.EncodeToString(sum[:])
			if checksum != r.Header.Get("X-Amz-Checksum-Sha256") {
				t.Error("part checksum mismatch")
			}
			mu.Lock()
			attempts[number]++
			if number == 2 && attempts[number] == 1 {
				mu.Unlock()
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, "<Error><Code>SlowDown</Code></Error>")
				return
			}
			active++
			peak = max(peak, active)
			if active == uploadConcurrency {
				close(allStarted)
			}
			mu.Unlock()
			select {
			case <-allStarted:
			case <-r.Context().Done():
				return
			}
			mu.Lock()
			active--
			parts[number] = body
			mu.Unlock()
			w.Header().Set("ETag", fmt.Sprintf("\"part-%d\"", number))
			w.Header().Set("X-Amz-Checksum-Sha256", checksum)
		case r.Method == http.MethodPost && r.URL.Query().Get("uploadId") == "test-upload":
			var result struct {
				Parts []struct {
					PartNumber     int
					ETag           string
					ChecksumSHA256 string
				} `xml:"Part"`
			}
			if err := xml.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(result.Parts) != 4 || len(parts) != 4 {
				t.Errorf("completion lost parts: %+v", result)
			}
			for i, part := range result.Parts {
				sum := sha256.Sum256(parts[i+1])
				if part.PartNumber != i+1 || part.ETag != fmt.Sprintf("\"part-%d\"", i+1) || part.ChecksumSHA256 != base64.StdEncoding.EncodeToString(sum[:]) {
					t.Errorf("incorrect completion part: %+v", part)
				}
			}
			completed = true
			_, _ = io.WriteString(w, "<CompleteMultipartUploadResult><ETag>done</ETag></CompleteMultipartUploadResult>")
		case r.Method == http.MethodHead:
			mu.Lock()
			defer mu.Unlock()
			checksum := sha256.New()
			for i := 1; i <= len(parts); i++ {
				sum := sha256.Sum256(parts[i])
				_, _ = checksum.Write(sum[:])
			}
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.Header().Set("X-Amz-Checksum-Type", "COMPOSITE")
			w.Header().Set("X-Amz-Checksum-Sha256", fmt.Sprintf("%s-%d", base64.StdEncoding.EncodeToString(checksum.Sum(nil)), len(parts)))
		case r.Method == http.MethodGet:
			mu.Lock()
			defer mu.Unlock()
			reads++
			if !completed {
				t.Error("object read before completion")
			}
			for i := 1; i <= len(parts); i++ {
				_, _ = w.Write(parts[i])
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := store.Upload(ctx, multipartTestID, file, size, digest); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(ctx, Manifest{ID: multipartTestID, SizeBytes: size, SHA256: digest}, file); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if reads != 0 {
		t.Error("checksum verification downloaded the archive")
	}
	mu.Unlock()
	if err := store.Download(ctx, Manifest{ID: multipartTestID, SizeBytes: size, SHA256: digest}, io.Discard); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak != uploadConcurrency || attempts[2] != 2 {
		t.Fatalf("parallelism=%d attempts=%v", peak, attempts)
	}
	for _, i := range []int{1, 3, 4} {
		if attempts[i] != 1 {
			t.Errorf("successful part %d was retried: %v", i, attempts)
		}
	}
}

func TestMultipartSnapshotAbortsOnFailure(t *testing.T) {
	for _, failure := range []string{"part", "complete", "cancel", "checksum", "short-source"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			aborted, completed, active := false, false, 0
			store := multipartTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				switch {
				case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
					_, _ = io.WriteString(w, "<InitiateMultipartUploadResult><UploadId>failed-upload</UploadId></InitiateMultipartUploadResult>")
				case r.Method == http.MethodDelete:
					mu.Lock()
					if active != 0 {
						t.Error("aborted before workers finished")
					}
					aborted = true
					mu.Unlock()
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPut:
					mu.Lock()
					active++
					mu.Unlock()
					defer func() { mu.Lock(); active--; mu.Unlock() }()
					_, _ = io.Copy(io.Discard, r.Body)
					if failure == "cancel" {
						cancel()
						return
					}
					if failure == "part" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, "<Error><Code>InvalidRequest</Code></Error>")
						return
					}
					w.Header().Set("ETag", "\"part\"")
					if failure == "checksum" {
						w.Header().Set("X-Amz-Checksum-Sha256", "incorrect")
					}
				case r.Method == http.MethodPost:
					mu.Lock()
					completed = true
					mu.Unlock()
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, "<Error><Code>InvalidPart</Code></Error>")
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			data := make([]byte, uploadPartSize+1)
			sum := sha256.Sum256(data)
			size := int64(len(data))
			if failure == "short-source" {
				size++
			}
			err := store.Upload(ctx, multipartTestID, bytes.NewReader(data), size, hex.EncodeToString(sum[:]))
			if err == nil {
				t.Fatal("failed multipart upload succeeded")
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !aborted || completed != (failure == "complete") {
				t.Fatalf("aborted=%v completed=%v error=%v", aborted, completed, err)
			}
		})
	}
}
