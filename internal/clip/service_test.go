package clip

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/scarletmu/openwhisker/internal/policy"
	"github.com/scarletmu/openwhisker/internal/storage"
)

type recordingEnrich struct{ calls [][2]string }

func (r *recordingEnrich) Enqueue(rawPath, parentJobID string) error {
	r.calls = append(r.calls, [2]string{rawPath, parentJobID})
	return nil
}

func newTestService(t *testing.T, vaultRoot string) (*Service, *storage.Store, *recordingEnrich) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	enrich := &recordingEnrich{}
	svc, err := NewService(Config{
		Store:       store,
		Conventions: policy.DefaultConventions(),
		VaultRoot:   vaultRoot,
		Enrich:      enrich,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, enrich
}

func TestClipSkeletonThenProcessRewritesNote(t *testing.T) {
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	svc, store, enrich := newTestService(t, vaultRoot)
	at := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	svc.WithClock(func() time.Time { return at }).WithFetch(func(context.Context, *http.Client, string) (string, error) {
		return `<html><head><title>Raft Paper</title></head><body><article><h1>Raft</h1><p>A consensus algorithm.</p></article></body></html>`, nil
	})

	notePath, jobID, err := svc.Clip(context.Background(), "https://example.com/raft", "matrix:@me", "matrix:@me")
	if err != nil {
		t.Fatalf("Clip() error = %v", err)
	}
	if !strings.HasPrefix(notePath, "Raw/Sources/2026-06-15-") {
		t.Fatalf("notePath = %q, want under Raw/Sources/ with date prefix", notePath)
	}

	// Skeleton landed in status: clipping and is queued.
	skeleton := mustRead(t, vaultRoot, notePath)
	if !strings.Contains(skeleton, "status: clipping") || !strings.Contains(skeleton, "raw_kind: web-clip") {
		t.Fatalf("skeleton frontmatter wrong:\n%s", skeleton)
	}
	job, ok := dequeue(svc)
	if !ok || job.NotePath != notePath || job.ParentJobID != jobID {
		t.Fatalf("queued job = %+v ok=%v", job, ok)
	}

	if err := svc.ProcessClip(context.Background(), job); err != nil {
		t.Fatalf("ProcessClip() error = %v", err)
	}
	final := mustRead(t, vaultRoot, notePath)
	for _, want := range []string{
		"status: clipped",
		`title: "Raft Paper"`,
		"# Raft Paper",
		"来源：https://example.com/raft",
		"A consensus algorithm.",
		"url: \"https://example.com/raft\"",
	} {
		if !strings.Contains(final, want) {
			t.Fatalf("clipped note missing %q:\n%s", want, final)
		}
	}

	// Background tagging scheduled for the finished clip, and a result outbox
	// message queued for the user.
	if len(enrich.calls) != 1 || enrich.calls[0][0] != notePath {
		t.Fatalf("enrich calls = %+v, want one for %s", enrich.calls, notePath)
	}
	out, err := store.ListPendingOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !strings.Contains(out[0].Body, "剪藏完成") {
		t.Fatalf("outbox = %+v, want one 剪藏完成 message", out)
	}
}

func TestClipProcessFailureRecordsFailedStatus(t *testing.T) {
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	svc, store, enrich := newTestService(t, vaultRoot)
	svc.WithFetch(func(context.Context, *http.Client, string) (string, error) {
		return "", errors.New("boom")
	})

	notePath, _, err := svc.Clip(context.Background(), "https://example.com/down", "cli", "")
	if err != nil {
		t.Fatalf("Clip() error = %v", err)
	}
	job, _ := dequeue(svc)
	if perr := svc.ProcessClip(context.Background(), job); perr == nil {
		t.Fatalf("ProcessClip() error = nil, want the fetch failure surfaced")
	}
	final := mustRead(t, vaultRoot, notePath)
	if !strings.Contains(final, "status: clip-failed") || !strings.Contains(final, "boom") {
		t.Fatalf("failed clip note wrong:\n%s", final)
	}
	// Failed clips are not tagged.
	if len(enrich.calls) != 0 {
		t.Fatalf("enrich called on a failed clip: %+v", enrich.calls)
	}
	out, _ := store.ListPendingOutbox(10)
	if len(out) != 1 || out[0].Kind != "error" {
		t.Fatalf("outbox = %+v, want one error message", out)
	}
}

func TestValidateClipURLRejectsPrivateTargets(t *testing.T) {
	for _, bad := range []string{
		"http://localhost/x",
		"http://127.0.0.1/x",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.5/admin",
		"ftp://example.com/x",
		"not a url",
	} {
		if err := validateClipURL(bad); err == nil {
			t.Errorf("validateClipURL(%q) = nil, want rejection", bad)
		}
	}
	if err := validateClipURL("https://example.com/ok"); err != nil {
		t.Errorf("validateClipURL(public) = %v, want nil", err)
	}
}

func mustRead(t *testing.T, vaultRoot, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func dequeue(svc *Service) (Job, bool) {
	select {
	case job := <-svc.Queue().C():
		return job, true
	default:
		return Job{}, false
	}
}
