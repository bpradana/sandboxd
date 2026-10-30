//go:build e2e && linux

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type snapshotTestCloud struct {
	*httptest.Server
	multipartUploads      atomic.Int32
	rangedDownloads       atomic.Int32
	checksumVerifications atomic.Int32
}

type snapshotTestObject struct {
	data                   []byte
	checksum, checksumType string
}

func snapshotCloudFixture(t *testing.T) *snapshotTestCloud {
	t.Helper()
	var mu sync.Mutex
	objects := map[string]snapshotTestObject{}
	uploads := map[string]map[int][]byte{}
	cloud := &snapshotTestCloud{}
	cloud.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=snapshot-ui-key/") {
			t.Error("saved connection credentials were not used")
		}
		mu.Lock()
		defer mu.Unlock()
		query, key := r.URL.Query(), r.URL.Path
		if query.Get("list-type") == "2" {
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
		if r.Method == http.MethodPost && query.Has("uploads") {
			id := fmt.Sprintf("upload-%d", cloud.multipartUploads.Add(1))
			uploads[id] = map[int][]byte{}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
			return
		}
		if uploadID := query.Get("uploadId"); uploadID != "" {
			switch r.Method {
			case http.MethodPut:
				data, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(500)
					return
				}
				part, _ := strconv.Atoi(query.Get("partNumber"))
				sum := sha256.Sum256(data)
				checksum := base64.StdEncoding.EncodeToString(sum[:])
				if checksum != r.Header.Get("X-Amz-Checksum-Sha256") {
					t.Error("invalid uploaded part checksum")
				}
				uploads[uploadID][part] = data
				w.Header().Set("ETag", fmt.Sprintf("\"part-%d\"", part))
				w.Header().Set("X-Amz-Checksum-Sha256", checksum)
			case http.MethodPost:
				var completion struct {
					Parts []struct {
						PartNumber     int
						ChecksumSHA256 string
					} `xml:"Part"`
				}
				if err := xml.NewDecoder(r.Body).Decode(&completion); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				var data bytes.Buffer
				checksum := sha256.New()
				for i, part := range completion.Parts {
					body := uploads[uploadID][part.PartNumber]
					sum := sha256.Sum256(body)
					if part.PartNumber != i+1 || part.ChecksumSHA256 != base64.StdEncoding.EncodeToString(sum[:]) {
						t.Error("invalid multipart completion")
					}
					_, _ = data.Write(body)
					_, _ = checksum.Write(sum[:])
				}
				objects[key] = snapshotTestObject{data: data.Bytes(), checksum: fmt.Sprintf("%s-%d", base64.StdEncoding.EncodeToString(checksum.Sum(nil)), len(completion.Parts)), checksumType: "COMPOSITE"}
				delete(uploads, uploadID)
				w.Header().Set("Content-Type", "application/xml")
				_, _ = io.WriteString(w, "<CompleteMultipartUploadResult><ETag>done</ETag></CompleteMultipartUploadResult>")
			case http.MethodDelete:
				delete(uploads, uploadID)
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			objects[key] = snapshotTestObject{data: data, checksum: r.Header.Get("X-Amz-Checksum-Sha256"), checksumType: "FULL_OBJECT"}
		case http.MethodGet, http.MethodHead:
			object, ok := objects[key]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
				return
			}
			sum := sha256.Sum256(object.data)
			etag := fmt.Sprintf("\"%x\"", sum)
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Length", strconv.Itoa(len(object.data)))
			if r.Method == http.MethodHead {
				if r.Header.Get("X-Amz-Checksum-Mode") == "ENABLED" && object.checksum != "" {
					cloud.checksumVerifications.Add(1)
					w.Header().Set("X-Amz-Checksum-Sha256", object.checksum)
					w.Header().Set("X-Amz-Checksum-Type", object.checksumType)
				}
				return
			}
			if requested := r.Header.Get("Range"); requested != "" {
				if r.Header.Get("If-Match") != etag {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
				var first, last int64
				if _, err := fmt.Sscanf(requested, "bytes=%d-%d", &first, &last); err != nil || first < 0 || last < first || last >= int64(len(object.data)) {
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				cloud.rangedDownloads.Add(1)
				w.Header().Set("Content-Length", strconv.FormatInt(last-first+1, 10))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(object.data)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(object.data[first : last+1])
				return
			}
			_, _ = w.Write(object.data)
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	return cloud
}
