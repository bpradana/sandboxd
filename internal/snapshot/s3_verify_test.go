package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestS3VerifyChecksumOrReadback(t *testing.T) {
	data := []byte("verified snapshot archive")
	sum := sha256.Sum256(data)
	m := Manifest{ID: multipartTestID, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	for _, mode := range []string{"checksum", "missing", "unsupported", "bad-checksum", "bad-size", "bad-readback"} {
		t.Run(mode, func(t *testing.T) {
			var reads atomic.Int32
			store := multipartTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					if r.Header.Get("X-Amz-Checksum-Mode") != "ENABLED" {
						t.Error("checksum mode not requested")
					}
					if mode == "unsupported" {
						w.WriteHeader(http.StatusMethodNotAllowed)
						return
					}
					w.Header().Set("Content-Length", strconv.Itoa(len(data)))
					if mode != "missing" && mode != "bad-readback" {
						w.Header().Set("X-Amz-Checksum-Sha256", base64.StdEncoding.EncodeToString(sum[:]))
						w.Header().Set("X-Amz-Checksum-Type", "FULL_OBJECT")
					}
					if mode == "bad-checksum" {
						w.Header().Set("X-Amz-Checksum-Sha256", "invalid")
					}
					if mode == "bad-size" {
						w.Header().Set("Content-Length", "1")
					}
					return
				}
				reads.Add(1)
				if mode == "bad-readback" {
					_, _ = io.WriteString(w, "corrupt")
					return
				}
				_, _ = w.Write(data)
			}))
			err := store.Verify(context.Background(), m, bytes.NewReader(data))
			bad := mode == "bad-checksum" || mode == "bad-size" || mode == "bad-readback"
			if bad && !errors.Is(err, ErrCorrupt) || !bad && err != nil {
				t.Fatalf("verification error: %v", err)
			}
			fallback := mode == "missing" || mode == "unsupported" || mode == "bad-readback"
			if (reads.Load() == 1) != fallback {
				t.Fatalf("readbacks=%d, expected fallback=%v", reads.Load(), fallback)
			}
		})
	}
}
