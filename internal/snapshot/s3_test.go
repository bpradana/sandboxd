package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/config"
)

func TestS3SnapshotRoundTripAndIntegrity(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "snapshot-test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "snapshot-test-secret") // pragma: allowlist secret -- test credential for httptest only.
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("request is not signed")
		}
		if r.URL.Query().Get("list-type") == "2" {
			type entry struct {
				Key string `xml:"Key"`
			}
			result := struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []entry  `xml:"Contents"`
			}{}
			for key := range objects {
				result.Contents = append(result.Contents, entry{Key: strings.TrimPrefix(key, "/backups/")})
			}
			w.Header().Set("Content-Type", "application/xml")
			_ = xml.NewEncoder(w).Encode(result)
			return
		}
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			if want := r.Header.Get("X-Amz-Checksum-Sha256"); want != "" {
				sum := sha256.Sum256(data)
				if want != base64.StdEncoding.EncodeToString(sum[:]) {
					t.Error("incorrect upload checksum")
				}
			}
			objects[r.URL.Path] = data
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := objects[r.URL.Path]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
				return
			}
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()
	store, err := NewS3Store(context.Background(), config.SnapshotConfig{Enabled: true, Bucket: "backups", Region: "us-east-1", Endpoint: srv.URL, Prefix: "sandboxd/snapshots", PathStyle: true, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	data := []byte("portable filesystem archive")
	sum := sha256.Sum256(data)
	m := Manifest{Version: Version, ID: "snap-0123456789abcdef01234567", Status: Ready, CreatedAt: time.Now(), SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	if err := store.Upload(ctx, m.ID, bytes.NewReader(data), int64(len(data)), m.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := store.PutManifest(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetManifest(ctx, m.ID)
	if err != nil || got.SHA256 != m.SHA256 {
		t.Fatalf("manifest %+v error %v", got, err)
	}
	var dst bytes.Buffer
	if err := store.Download(ctx, got, &dst); err != nil || !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("download %q error %v", dst.Bytes(), err)
	}
	items, err := store.List(ctx)
	if err != nil || len(items) != 1 || items[0].ID != m.ID {
		t.Fatalf("list %+v error %v", items, err)
	}
	mu.Lock()
	objects["/backups/sandboxd/snapshots/"+m.ID+".tar.gz"] = []byte("tampered")
	mu.Unlock()
	if err := store.Download(ctx, m, io.Discard); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corruption accepted: %v", err)
	}
	m.SizeBytes = 1025
	if err := store.Download(ctx, m, io.Discard); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("unbounded download: %v", err)
	}
	if err := store.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetManifest(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing manifest error: %v", err)
	}
	if err := store.Delete(ctx, "../other"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("unsafe id: %v", err)
	}
}

func TestInterruptedSnapshotExpires(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, status := range []string{Capturing, Uploading} {
		m := Manifest{Status: status, DeadlineAt: now.Add(-time.Second)}
		if m.EffectiveStatus(now) != Failed {
			t.Fatalf("interrupted job still %s", status)
		}
	}
	if (Manifest{Status: Ready}).EffectiveStatus(now) != Ready {
		t.Fatal("ready backup expired")
	}
}
