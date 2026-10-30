package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestSnapshotParallelDownload(t *testing.T) {
	data := make([]byte, 3*uploadPartSize+123)
	for i := range data {
		data[i] = byte(int64(i)/uploadPartSize + 1)
	}
	sum := sha256.Sum256(data)
	m := Manifest{ID: multipartTestID, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	var mu sync.Mutex
	active, peak, ranges := 0, 0, 0
	started := make(chan struct{})
	store := multipartTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("ETag", "\"archive\"")
		if r.Method == http.MethodHead {
			return
		}
		if r.Header.Get("If-Match") != "\"archive\"" {
			t.Error("download was not pinned to an object identity")
		}
		var first, last int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil || first < 0 || last >= int64(len(data)) || last < first {
			t.Error("invalid range")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		active++
		peak = max(peak, active)
		ranges++
		if active == uploadConcurrency {
			close(started)
		}
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		select {
		case <-started:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(last-first+1, 10))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[first : last+1])
	}))
	file, err := os.CreateTemp(t.TempDir(), "download")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	// Store.Download must retain normal io.Writer semantics at a nonzero
	// starting cursor, even though individual ranges use WriteAt.
	prefix := []byte("prefix")
	if _, err := file.Write(prefix); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := store.Download(ctx, m, file); err != nil {
		t.Fatal(err)
	}
	position, err := file.Seek(0, io.SeekCurrent)
	if err != nil || position != int64(len(prefix)+len(data)) {
		t.Fatalf("file cursor=%d error=%v", position, err)
	}
	gotPrefix := make([]byte, len(prefix))
	if _, err := file.ReadAt(gotPrefix, 0); err != nil || !bytes.Equal(gotPrefix, prefix) {
		t.Fatal("prefix overwritten")
	}
	mu.Lock()
	defer mu.Unlock()
	if peak != uploadConcurrency || ranges != 4 {
		t.Fatalf("parallelism=%d requests=%d", peak, ranges)
	}
}

func TestSnapshotParallelDownloadRejectsCorruption(t *testing.T) {
	data := make([]byte, uploadPartSize+1)
	sum := sha256.Sum256(data)
	m := Manifest{ID: multipartTestID, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	for _, mode := range []string{"bad-range", "bad-length", "ignored-range", "short", "corrupt", "changed", "unsupported-head"} {
		t.Run(mode, func(t *testing.T) {
			store := multipartTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				w.Header().Set("ETag", "\"original\"")
				if r.Method == http.MethodHead {
					if mode == "unsupported-head" {
						w.WriteHeader(http.StatusMethodNotAllowed)
					}
					return
				}
				if mode == "unsupported-head" {
					if r.Header.Get("Range") != "" {
						t.Error("missing HEAD support did not fall back")
					}
					_, _ = w.Write(data)
					return
				}
				if mode == "changed" {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
				var first, last int64
				_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last)
				contentRange := fmt.Sprintf("bytes %d-%d/%d", first, last, len(data))
				if mode == "ignored-range" {
					_, _ = w.Write(data)
					return
				}
				if mode == "bad-range" {
					contentRange = "bytes 0-1/2"
				}
				length := last - first + 1
				w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
				if mode == "bad-length" {
					w.Header().Set("Content-Length", strconv.FormatInt(length+1, 10))
				}
				w.Header().Set("Content-Range", contentRange)
				w.WriteHeader(http.StatusPartialContent)
				if mode == "short" {
					return
				}
				if mode == "corrupt" {
					_, _ = w.Write([]byte{1})
					_, _ = w.Write(data[first:last])
					return
				}
				_, _ = w.Write(data[first : last+1])
			}))
			file, err := os.CreateTemp(t.TempDir(), "download")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			err = store.Download(context.Background(), m, file)
			if mode == "unsupported-head" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("corrupt or inconsistent object accepted")
			} else if mode == "corrupt" && !errors.Is(err, ErrCorrupt) {
				t.Fatalf("checksum mismatch not reported: %v", err)
			}
		})
	}
}
