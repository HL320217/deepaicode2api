package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"deepaicode2api/internal/deepai"
)

func TestResourceKeysAreIndependentSnapshots(t *testing.T) {
	s, err := OpenWithKey(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	k := s.AddKey("original", "")
	snapshot := s.Keys()
	s.ValidKey(k.Key)
	if snapshot[0].Requests != 0 {
		t.Fatal("key snapshot changed after authentication")
	}
	snapshot[0].Name = "external change"
	if s.Keys()[0].Name != "original" {
		t.Fatal("snapshot changed stored key")
	}
}

func TestResourceRollbackPreservesDiskAndUnrelatedState(t *testing.T) {
	s, err := OpenWithKey(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.UpsertAccount(&deepai.Account{ID: "a", APIKey: "upstream", Enabled: true, Failures: 4, Status: "error", LastErr: "previous"})
	k := s.AddKey("original", "")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.Path()+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	name := "changed"
	for _, mutate := range []func() error{
		func() error { _, err := s.BatchAccounts([]string{"a"}, "clear-errors", ""); return err },
		func() error { return s.UpdateKeyResource(k.PublicID(), ResourceUpdate{Name: &name}) },
		func() error { return s.DeleteKeyResource(k.PublicID(), false) },
	} {
		if err := mutate(); err == nil {
			t.Fatal("write should fail")
		}
	}
	if a := s.AccountCopy("a"); a.Failures != 4 || a.LastErr != "previous" {
		t.Fatalf("rollback lost account state: %+v", a)
	}
	if keys := s.Keys(); len(keys) != 1 || keys[0].Name != "original" {
		t.Fatal("rollback lost key state")
	}
	after, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed write changed persisted state")
	}
}

func TestResourceConcurrentUpdatesAndSavePreserveAllChanges(t *testing.T) {
	s, err := OpenWithKey(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.UpsertAccount(&deepai.Account{ID: "a", APIKey: "upstream", Enabled: true})
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(3)
	go func() {
		defer wg.Done()
		name := "new"
		errs <- s.UpdateAccountResource("a", ResourceUpdate{Name: &name})
	}()
	go func() {
		defer wg.Done()
		group := "team"
		errs <- s.UpdateAccountResource("a", ResourceUpdate{Group: &group})
	}()
	go func() { defer wg.Done(); errs <- s.Save() }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	disk, err := OpenWithKey(s.Path(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a := disk.AccountCopy("a")
	if a.Name != "new" || a.Group != "team" {
		t.Fatalf("lost update: %+v", a)
	}
}

func TestResourceEnrollmentAndKeyUpdatesPreserveVerifiedRuntime(t *testing.T) {
	s, err := OpenWithKey(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.UpsertAccount(&deepai.Account{ID: "a", APIKey: "old-key", Email: "same@example.com", Name: "kept name", MachineID: "kept-machine", Status: "error", Failures: 5, LastErr: "old failure"})
	key := s.AddKey("old name", "")
	candidate := &deepai.Account{ID: "new-id", APIKey: "new-key", Email: "same@example.com", EmailVerified: true, Plan: "new-plan", Status: "ok", LastUsed: 111, LastVerified: 222, Theos: &deepai.Allowance{Enabled: true, Balance: "100"}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		id, isNew, err := s.EnrollAccount(candidate)
		if err == nil && (id != "a" || isNew) {
			t.Error("duplicate enrollment did not preserve identity")
		}
		errs <- err
	}()
	go func() {
		defer wg.Done()
		name := "new key name"
		errs <- s.UpdateKeyResource(key.PublicID(), ResourceUpdate{Name: &name})
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// The input remains caller-owned, so later caller changes cannot mutate state.
	candidate.Theos.Balance = "caller change"
	disk, err := OpenWithKey(s.Path(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []*Store{s, disk} {
		a := st.AccountCopy("a")
		if len(st.Accounts()) != 1 || a.APIKey != "new-key" || a.Status != "ok" || a.Failures != 0 || a.LastErr != "" || a.LastUsed != 111 || a.LastVerified != 222 || !a.EmailVerified || a.Plan != "new-plan" || a.Theos.Balance != "100" || a.Name != "kept name" || a.MachineID != "kept-machine" {
			t.Fatalf("enrollment lost verified state or identity: %+v", a)
		}
		if st.Keys()[0].Name != "new key name" {
			t.Fatal("enrollment lost concurrent key update")
		}
	}
}
