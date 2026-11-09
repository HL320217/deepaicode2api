package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"deepaicode2api/internal/deepai"
)

func TestGooglePoolMergeReserveAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenWithKey(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	added, updated := s.MergeGoogleAccounts([]GoogleAccount{{Email: "a@school.edu", Password: "private-a"}, {Email: "b@company.org", Password: "private-b"}})
	if added != 2 || updated != 0 {
		t.Fatal("incorrect import counts")
	}
	selected := s.ReserveGoogleAccounts(1, false)
	if len(selected) != 1 || selected[0].Password != "private-a" {
		t.Fatal("failed to reserve credentials")
	}
	s.SetGoogleAccountResult("a@school.edu", "authorized", "done", "", "acct-a")
	s.MergeGoogleAccounts([]GoogleAccount{{Email: "a@school.edu", Password: "new-password"}})
	views := s.GoogleAccounts()
	if views[0].Status != "authorized" || views[0].AccountID != "acct-a" {
		t.Fatal("reimport lost completed state")
	}
	raw, _ := json.Marshal(views)
	if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "private-") {
		t.Fatal("pool response leaks credentials")
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithKey(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	selected = reopened.ReserveGoogleAccounts(10, false)
	if len(selected) != 1 || selected[0].Email != "b@company.org" {
		t.Fatal("completed account was reused")
	}
}

func TestGoogleMachinePersistsAcrossRetryAndReimport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := OpenWithKey(path, nil)
	s.MergeGoogleAccounts([]GoogleAccount{{Email: "a@school.edu", Password: "secret"}, {Email: "b@school.edu", Password: "secret"}})
	first, err := s.EnsureGoogleMachineID("a@school.edu", "key-a")
	if err != nil || !deepai.UUIDPattern.MatchString(first) {
		t.Fatalf("missing machine identity: %v", err)
	}
	second, err := s.EnsureGoogleMachineID("b@school.edu", "key-b")
	if err != nil || second == first || second == "" {
		t.Fatal("Google accounts share an identity")
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithKey(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened.MergeGoogleAccounts([]GoogleAccount{{Email: "a@school.edu", Password: "new-password"}})
	again, err := reopened.EnsureGoogleMachineID("a@school.edu", "rotated-key-a")
	if err != nil || again != first {
		t.Fatal("retry or key rotation changed the saved identity")
	}
}

func TestGoogleMachinePreservesExistingAccountIdentity(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		s, _ := OpenWithKey(filepath.Join(t.TempDir(), "state.json"), nil)
		s.MergeGoogleAccounts([]GoogleAccount{{Email: "a@school.edu", Password: "secret"}})
		key, email := "existing-key", "a@school.edu"
		if sameKey {
			email = ""
		} else {
			key = "old-key"
		}
		s.UpsertAccount(&deepai.Account{ID: "existing", APIKey: key, Email: email, MachineID: "existing-machine"})
		id, err := s.EnsureGoogleMachineID("a@school.edu", "existing-key")
		if err != nil || id != "existing-machine" {
			t.Fatalf("lost existing identity (same key %v): %v", sameKey, err)
		}
	}
}

func TestGooglePoolRetryAndInterruptedWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := OpenWithKey(path, nil)
	s.MergeGoogleAccounts([]GoogleAccount{{Email: "a@company.org", Password: "secret"}})
	s.ReserveGoogleAccounts(1, false)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithKey(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.ReserveGoogleAccounts(1, false)) != 0 {
		t.Fatal("interrupted account automatically reused")
	}
	if len(reopened.ReserveGoogleAccounts(1, true)) != 1 {
		t.Fatal("explicit retry did not release interrupted account")
	}
}
