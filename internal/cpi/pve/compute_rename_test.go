package pve

import "testing"

// TestRenameParams pins the config write that renames a guest.
//
// The recycle operation builds its replacement under a transient name so
// name-based discovery and teardown are not confused while both guests are
// live, then renames it once the original is gone. Without a working rename
// the bloc is left with a bastion permanently called "<bloc>-bastion-next",
// which every name-based lookup then fails to find.
func TestRenameParams(t *testing.T) {
	t.Parallel()

	params := renameParams("ocfp-lab-bastion")

	got, ok := params["name"].(string)
	if !ok {
		t.Fatalf("params carry no name: %v", params)
	}

	if got != "ocfp-lab-bastion" {
		t.Errorf("name = %q, want ocfp-lab-bastion", got)
	}

	if len(params) != 1 {
		t.Errorf("rename must write only the name, got %v", params)
	}
}

// TestVzdumpParams pins the archive request. mode=snapshot keeps the guest
// running (or, for a stopped guest, avoids a needless start), and the archive
// is marked protected so a prune cannot remove the copy a recycle depends on.
func TestVzdumpParams(t *testing.T) {
	t.Parallel()

	params := vzdumpParams(100, "nfs-backup", "pre-recycle")

	checks := map[string]interface{}{
		"vmid":      100,
		"storage":   "nfs-backup",
		"mode":      "snapshot",
		"compress":  "zstd",
		"protected": 1,
	}

	for key, want := range checks {
		if got := params[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}

	if notes, _ := params["notes-template"].(string); notes != "pre-recycle" {
		t.Errorf("notes-template = %v, want pre-recycle", params["notes-template"])
	}
}
