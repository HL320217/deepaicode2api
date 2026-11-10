package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoogleDeletedCredentialsRetainAuthorizationOnReimport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenWithKey(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.MergeGoogleAccounts([]GoogleAccount{{Email: "a@example.com", Password: "private"}})
	s.SetGoogleAccountResult("a@example.com", "authorized", "done", "finished", "linked")
	if _, err := s.DeleteGoogleCredentials([]string{"a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if len(s.GoogleAccounts()) != 0 || len(s.ReserveGoogleAccounts(10, true)) != 0 {
		t.Fatal("deleted credentials remained usable")
	}
	s, err = OpenWithKey(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, u, c, err := s.ImportGoogleAccounts([]GoogleAccount{{Email: "a@example.com", Password: "replacement"}}, false)
	if err != nil || a != 1 || u != 0 || c != 1 {
		t.Fatalf("reimport counts: %d %d %d %v", a, u, c, err)
	}
	view := s.GoogleAccounts()[0]
	if view.Status != "authorized" || view.AccountID != "linked" || len(s.ReserveGoogleAccounts(1, true)) != 0 {
		t.Fatal("reimport lost historical authorization")
	}
}

func TestGooglePreviewDoesNotWriteOrReplacePassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenWithKey(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.ImportGoogleAccounts([]GoogleAccount{{Email: "a@example.com", Password: "old"}}, false); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	a, u, c, err := s.ImportGoogleAccounts([]GoogleAccount{{Email: "a@example.com", Password: "new"}, {Email: "b@example.com", Password: "new"}}, true)
	after, _ := os.ReadFile(path)
	if err != nil || a != 1 || u != 1 || c != 2 || string(before) != string(after) {
		t.Fatal("preview changed persistent state or counts")
	}
	got := s.ReserveGoogleAccounts(10, false)
	if len(got) != 1 || got[0].Password != "old" {
		t.Fatal("preview changed credentials")
	}
}
