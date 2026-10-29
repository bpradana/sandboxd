//go:build e2e

package e2e

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"

	"github.com/bpradana/sandboxd/internal/presentation"
)

func (s *e2eSuite) runFilesCase(t *testing.T) {
	sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
	files := "/api/v1/sandboxes/" + sandbox.ID + "/files"
	q := func(path string) string { return "?path=" + url.QueryEscape(path) }

	t.Run("upload and download round-trip binary content", func(t *testing.T) {
		content := []byte{0, 1, 2, 255, '\n', 'x'}
		status, raw := s.raw(t, http.MethodPut, files+q("/home/sandbox/blob.bin")+"&mode=0600", content, s.apiKey)
		if status != http.StatusOK {
			t.Fatalf("upload status %d: %s", status, raw)
		}
		status, raw = s.raw(t, http.MethodGet, files+q("/home/sandbox/blob.bin"), nil, s.apiKey)
		if status != http.StatusOK || !bytes.Equal(raw, content) {
			t.Fatalf("download status %d: %v", status, raw)
		}
		if out := s.mustSucceed(t, sandbox.ID, "stat -c '%U %a %s' /home/sandbox/blob.bin"); out != "sandbox 600 6\n" {
			t.Fatalf("owner/mode/size = %q", out)
		}
	})

	t.Run("symlinks resolve inside the sandbox, never on the host", func(t *testing.T) {
		s.mustSucceed(t, sandbox.ID, "cd /home/sandbox && ln -s /etc/hostname abs && ln -s ../../../../../../../etc/hostname rel && ln -s /proc/1/root/etc/hostname magic")
		for _, link := range []string{"/home/sandbox/abs", "/home/sandbox/rel"} {
			status, raw := s.raw(t, http.MethodGet, files+q(link), nil, s.apiKey)
			if status != http.StatusOK || string(raw) != "sandbox\n" {
				t.Fatalf("%s: status %d body %q (must be the sandbox's /etc/hostname)", link, status, raw)
			}
		}
		if status, raw := s.raw(t, http.MethodGet, files+q("/home/sandbox/magic"), nil, s.apiKey); status == http.StatusOK {
			t.Fatalf("magic link resolved: %q", raw)
		}
		if status, raw := s.raw(t, http.MethodGet, files+q("/../../../etc/hostname"), nil, s.apiKey); status != http.StatusOK || string(raw) != "sandbox\n" {
			t.Fatalf("dot-dot path: status %d body %q", status, raw)
		}
	})

	t.Run("writes through a symlink stay inside the sandbox", func(t *testing.T) {
		s.mustSucceed(t, sandbox.ID, "ln -s /etc/passwd /home/sandbox/passwd-link")
		status, raw := s.raw(t, http.MethodPut, files+q("/home/sandbox/passwd-link"), []byte("replaced"), s.apiKey)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		if out := s.mustSucceed(t, sandbox.ID, "head -c 4 /etc/passwd; echo; cat /home/sandbox/passwd-link"); out != "root\nreplaced" {
			t.Fatalf("result = %q (the link itself should be replaced, /etc/passwd untouched)", out)
		}
	})

	t.Run("mkdir, list, stat, and remove", func(t *testing.T) {
		status, envelope := s.call(t, http.MethodPost, files+"/mkdir", presentation.MkdirRequest{Path: "/home/sandbox/a/b/c", Parents: true})
		requireStatus(t, status, http.StatusCreated, envelope)
		s.raw(t, http.MethodPut, files+q("/home/sandbox/a/b/c/f.txt"), []byte("hi"), s.apiKey)

		status, envelope = s.call(t, http.MethodGet, files+"/list"+q("/home/sandbox/a/b/c"), nil)
		requireStatus(t, status, http.StatusOK, envelope)
		entries := decode[[]presentation.FileInfoResponse](t, envelope)
		if len(entries) != 1 || entries[0].Name != "f.txt" || entries[0].Size != 2 || entries[0].Type != "file" {
			t.Fatalf("entries = %+v", entries)
		}

		status, envelope = s.call(t, http.MethodDelete, files+q("/home/sandbox/a"), nil)
		if status != http.StatusConflict {
			t.Fatalf("non-recursive delete of a non-empty dir: status %d", status)
		}
		status, envelope = s.call(t, http.MethodDelete, files+q("/home/sandbox/a")+"&recursive=true", nil)
		requireStatus(t, status, http.StatusOK, envelope)
		status, envelope = s.call(t, http.MethodGet, files+"/stat"+q("/home/sandbox/a"), nil)
		requireStatus(t, status, http.StatusNotFound, envelope)
	})

	t.Run("uploads count against storage", func(t *testing.T) {
		small := s.createSandbox(t, presentation.CreateSandboxRequest{StorageMB: 16})
		status, raw := s.raw(t, http.MethodPut, "/api/v1/sandboxes/"+small.ID+"/files"+q("/tmp/big"), make([]byte, 32<<20), s.apiKey)
		if status != http.StatusInsufficientStorage {
			t.Fatalf("32 MiB upload into 16 MiB storage: status %d %s", status, raw)
		}
	})
}
