package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestAdapterServiceHandlesMatrixRawDedupeAndApproval(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewAdapterService(store, vaultRoot)

	raw, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-1",
		Sender:  "@user:example.test",
		Text:    "Phase4 matrix raw capture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw.Status != model.JobStatusDone || raw.JobID == "" {
		t.Fatalf("raw response = %+v, want done job", raw)
	}
	duplicate, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-1",
		Sender:  "@user:example.test",
		Text:    "duplicate should not create another job",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate {
		t.Fatalf("duplicate response = %+v, want duplicate", duplicate)
	}

	organized, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-2",
		Sender:  "@user:example.test",
		Text:    "/organize last",
	})
	if err != nil {
		t.Fatal(err)
	}
	if organized.Status != model.PlanStatusAwaitingApproval || organized.PlanID == "" {
		t.Fatalf("organize response = %+v, want awaiting approval", organized)
	}
	diff, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-3",
		Sender:  "@user:example.test",
		Text:    "/diff " + organized.PlanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Body, "create_note") {
		t.Fatalf("diff body = %q, want create_note", diff.Body)
	}
	approved, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-4",
		Sender:  "@user:example.test",
		Text:    "/approve " + organized.PlanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != model.PlanStatusApplied {
		t.Fatalf("approve response = %+v, want applied", approved)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCount(t, db, "wiki_jobs", 2)
	assertCount(t, db, "adapter_events", 4)
	pending, err := service.PullOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 3 {
		t.Fatalf("pending outbox count = %d, want 3", len(pending))
	}
	if err := service.MarkOutboxDelivered(pending[0].ID); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterServiceHandlesOrganizeToday(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewAdapterService(store, vaultRoot)
	for _, text := range []string{"today raw one", "today raw two"} {
		if _, err := service.HandleText(context.Background(), AdapterRequest{
			Adapter: model.AdapterMatrix,
			EventID: "$raw-" + strings.ReplaceAll(text, " ", "-"),
			Sender:  "@user:example.test",
			Text:    text,
		}); err != nil {
			t.Fatal(err)
		}
	}
	organized, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$organize-today",
		Sender:  "@user:example.test",
		Text:    "/organize today",
	})
	if err != nil {
		t.Fatal(err)
	}
	if organized.Status != model.PlanStatusAwaitingApproval || organized.PlanID == "" {
		t.Fatalf("organize today response = %+v, want awaiting approval", organized)
	}
	if !strings.Contains(organized.Body, "2 raw captures") {
		t.Fatalf("organize today body = %q, want raw count", organized.Body)
	}
}
