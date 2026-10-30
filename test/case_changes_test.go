//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// runChangesCase checks the change list against a real overlay: the kinds it
// reports are the kernel's own record of what the workload did, so they can
// only be confirmed by doing those things in a running sandbox.
func (s *e2eSuite) runChangesCase(t *testing.T) {
	sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
	path := "/api/v1/sandboxes/" + sandbox.ID + "/changes"

	changes := func(t *testing.T) map[string]presentation.SandboxChangeResponse {
		t.Helper()
		status, envelope := s.call(t, http.MethodGet, path, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		set := decode[presentation.SandboxChangesResponse](t, envelope)
		if set.Truncated {
			t.Fatal("this case changes a handful of files and must not truncate")
		}
		byPath := make(map[string]presentation.SandboxChangeResponse, len(set.Entries))
		for _, entry := range set.Entries {
			byPath[entry.Path] = entry
		}
		return byPath
	}

	t.Run("a fresh sandbox reports only what the daemon seeded", func(t *testing.T) {
		for path, entry := range changes(t) {
			if !entry.Seeded {
				t.Errorf("%s was reported before the workload did anything (%+v)", path, entry)
			}
		}
	})

	t.Run("creating, editing and deleting are each named", func(t *testing.T) {
		s.mustSucceed(t, sandbox.ID, "mkdir -p /workspace/out && echo built > /workspace/out/report.txt && chmod 600 /workspace/out/report.txt")
		// /etc/alpine-release ships in the image, so editing it copies it up
		// and removing it leaves a whiteout.
		s.mustSucceed(t, sandbox.ID, "echo edited >> /etc/alpine-release && cp /etc/alpine-release /tmp/kept && rm /etc/os-release")
		found := changes(t)

		for path, want := range map[string]string{
			"/workspace":                "added",
			"/workspace/out":            "added",
			"/workspace/out/report.txt": "added",
			"/etc/alpine-release":       "modified",
			"/etc/os-release":           "deleted",
		} {
			entry, ok := found[path]
			if !ok {
				t.Fatalf("%s is missing from the change list", path)
			}
			if entry.Kind != want {
				t.Errorf("%s: kind %q, want %q", path, entry.Kind, want)
			}
		}
		if entry := found["/workspace/out/report.txt"]; entry.Mode != "0600" || entry.Size != int64(len("built\n")) || entry.ModifiedAt == nil {
			t.Errorf("a created file should describe itself: %+v", entry)
		}
		if entry := found["/etc/os-release"]; entry.Mode != "" || entry.ModifiedAt != nil {
			t.Errorf("a deleted file has nothing left to describe: %+v", entry)
		}
		// /etc is in the image and only holds changed children.
		if _, ok := found["/etc"]; ok {
			t.Error("/etc is a copied-up parent, not a change")
		}
		// Nothing untouched may appear, however large the image is.
		if _, ok := found["/bin/busybox"]; ok {
			t.Error("a file the workload never touched is not a change")
		}
	})

	t.Run("a directory emptied of the image's files reads as replaced", func(t *testing.T) {
		s.mustSucceed(t, sandbox.ID, "rm -rf /media && mkdir /media && touch /media/mine")
		found := changes(t)
		if entry := found["/media"]; entry.Kind != "replaced" {
			t.Errorf("/media: kind %q, want replaced (%+v)", entry.Kind, entry)
		}
		if _, ok := found["/media/mine"]; !ok {
			t.Error("what was put in the replacement is still a change")
		}
	})

	t.Run("a destroyed sandbox has no changes to report", func(t *testing.T) {
		gone := s.createSandbox(t, presentation.CreateSandboxRequest{})
		status, envelope := s.call(t, http.MethodDelete, "/api/v1/sandboxes/"+gone.ID, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		status, envelope = s.call(t, http.MethodGet, "/api/v1/sandboxes/"+gone.ID+"/changes", nil)
		if status != http.StatusNotFound && status != http.StatusConflict {
			t.Fatalf("changes for a destroyed sandbox: status %d (%s)", status, envelope.Message)
		}
	})
}
