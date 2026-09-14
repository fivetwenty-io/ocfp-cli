package bootstrap_test

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/recycle"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// TestRecycleJournalSurvivesANewProcess is the assertion that makes resume
// real.
//
// state.AddResource only mutates the in-memory state; nothing reaches disk
// until Save is called. A journal that is not flushed is no journal at all:
// the next invocation reads nothing, concludes the run never started, and
// begins again from the top — stopping a VM it had already stopped and
// rebuilding a replacement it had already built.
func TestRecycleJournalSurvivesANewProcess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	write, err := state.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = write.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Name: "prod", Provider: "pve"}
	mgr := bootstrap.NewManager(cfg, &fakeProvWithStorage{}, write, &bootstrap.Options{
		BlocName: "prod", Provider: "pve",
	})

	cluster := bootstrap.NewBastionRecycleCluster(mgr, "")

	err = cluster.SaveJournal(context.Background(), &recycle.Journal{
		Phase:      recycle.PhaseHandedOver,
		Role:       "bastion",
		OriginalID: "100",
	})
	if err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}

	// A completely fresh manager, as a second invocation of the CLI would have.
	read, err := state.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = read.Load("prod"); err != nil {
		t.Fatal(err)
	}

	readMgr := bootstrap.NewManager(cfg, &fakeProvWithStorage{}, read, &bootstrap.Options{
		BlocName: "prod", Provider: "pve",
	})

	got, err := bootstrap.NewBastionRecycleCluster(readMgr, "").LoadJournal(context.Background())
	if err != nil {
		t.Fatalf("LoadJournal: %v", err)
	}

	if got == nil {
		t.Fatal("the journal did not survive; a resume would start again from the top")
	}

	if got.Phase != recycle.PhaseHandedOver {
		t.Errorf("phase = %q, want %q", got.Phase, recycle.PhaseHandedOver)
	}

	if got.OriginalID != "100" {
		t.Errorf("originalID = %q, want 100", got.OriginalID)
	}
}

// TestRecycleJournalClearedOnDisk asserts the clear is flushed too, so a
// finished run does not look like one still in progress.
func TestRecycleJournalClearedOnDisk(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	write, err := state.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = write.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Name: "prod", Provider: "pve"}
	mgr := bootstrap.NewManager(cfg, &fakeProvWithStorage{}, write, &bootstrap.Options{
		BlocName: "prod", Provider: "pve",
	})
	cluster := bootstrap.NewBastionRecycleCluster(mgr, "")

	ctx := context.Background()
	if err := cluster.SaveJournal(ctx, &recycle.Journal{Phase: recycle.PhaseBuilt}); err != nil {
		t.Fatal(err)
	}

	if err := cluster.ClearJournal(ctx); err != nil {
		t.Fatalf("ClearJournal: %v", err)
	}

	read, err := state.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = read.Load("prod"); err != nil {
		t.Fatal(err)
	}

	readMgr := bootstrap.NewManager(cfg, &fakeProvWithStorage{}, read, &bootstrap.Options{
		BlocName: "prod", Provider: "pve",
	})

	got, err := bootstrap.NewBastionRecycleCluster(readMgr, "").LoadJournal(ctx)
	if err != nil {
		t.Fatalf("LoadJournal: %v", err)
	}

	if got != nil {
		t.Errorf("a cleared journal came back from disk: %+v", got)
	}
}
