package server

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/store"
)

func TestResourceAccountUpdatePersistsAndIsIdempotent(t *testing.T) {
	s, _ := newTestServer(t, nil)
	for i := 0; i < 2; i++ {
		r := do(t, s, "POST", "/admin/api/accounts", "", `{"action":"update","id":"acc-1","name":"renamed","group":"team","enabled":false}`)
		if r.Code != 200 {
			t.Fatalf("update: %d %s", r.Code, r.Body.String())
		}
	}
	disk, err := store.OpenWithKey(s.Store.Path(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a := disk.AccountCopy("acc-1")
	if a.Name != "renamed" || a.Group != "team" || a.Enabled || a.Status != "ok" {
		t.Fatalf("wrong account: %+v", a)
	}
	r := do(t, s, "POST", "/admin/api/accounts", "", `{"action":"update","id":"acc-1","name":""}`)
	if r.Code != 200 || s.Store.AccountCopy("acc-1").Group != "team" {
		t.Fatalf("optional fields: %s", r.Body.String())
	}
}

func TestResourceAccountBatchReportsMissingAndValidates(t *testing.T) {
	s, _ := newTestServer(t, nil)
	r := do(t, s, "POST", "/admin/api/accounts", "", `{"action":"batch","ids":["acc-1","missing"],"operation":"disable"}`)
	if r.Code != 200 {
		t.Fatalf("batch: %d %s", r.Code, r.Body.String())
	}
	v := decodeJSON[struct {
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
		Results   []struct {
			ID    string `json:"id"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"results"`
	}](t, r.Body.Bytes())
	if v.Succeeded != 1 || v.Failed != 1 || len(v.Results) != 2 || !v.Results[0].OK || v.Results[1].Error == "" || s.Store.AccountCopy("acc-1").Enabled {
		t.Fatalf("wrong results: %+v", v)
	}
	for _, body := range []string{`{"action":"batch","ids":[],"operation":"enable"}`, `{"action":"batch","ids":["acc-1"],"operation":"unknown"}`, `{"action":"batch","ids":["acc-1"],"operation":"set-group","group":" "}`} {
		if r := do(t, s, "POST", "/admin/api/accounts", "", body); r.Code != 400 {
			t.Fatalf("invalid batch accepted: %d %s", r.Code, r.Body.String())
		}
	}
	for _, action := range []string{"update", "toggle", "remove", "clear-errors", "assign-machine", "set-group"} {
		if r := do(t, s, "POST", "/admin/api/accounts", "", `{"action":"`+action+`","id":"missing","group":"team"}`); r.Code != 404 {
			t.Errorf("missing %s: %d", action, r.Code)
		}
	}
}

func TestResourceKeyIDsAndEditingDoNotDiscloseSecrets(t *testing.T) {
	s, secret := newTestServer(t, nil)
	r := do(t, s, "GET", "/admin/api/keys", "", "")
	v := decodeJSON[struct {
		Keys []struct {
			ID string `json:"id"`
		}
	}](t, r.Body.Bytes())
	if len(v.Keys) != 1 || v.Keys[0].ID == "" {
		t.Fatalf("missing public ID: %s", r.Body.String())
	}
	id := v.Keys[0].ID
	for i := 0; i < 2; i++ {
		r = do(t, s, "PATCH", "/admin/api/keys", "", `{"id":"`+id+`","name":"new","group":"team","enabled":false}`)
		if r.Code != 200 || strings.Contains(r.Body.String(), secret) {
			t.Fatalf("patch: %d %s", r.Code, r.Body.String())
		}
	}
	disk, err := store.OpenWithKey(s.Store.Path(), nil)
	if err != nil {
		t.Fatal(err)
	}
	k := disk.Keys()[0]
	if k.Name != "new" || k.Group != "team" || k.Enabled {
		t.Fatalf("not persisted: %+v", k)
	}
	for _, path := range []string{"/admin/api/keys", "/admin/api/state"} {
		r = do(t, s, "GET", path, "", "")
		if strings.Contains(r.Body.String(), secret) || !strings.Contains(r.Body.String(), id) {
			t.Fatalf("unsafe or missing ID: %s", r.Body.String())
		}
	}
	r = do(t, s, "DELETE", "/admin/api/keys?id="+id, "", "")
	if r.Code != 200 || len(s.Store.Keys()) != 0 {
		t.Fatalf("delete: %s", r.Body.String())
	}
	r = do(t, s, "DELETE", "/admin/api/keys?id="+id, "", "")
	if r.Code != 404 {
		t.Fatalf("missing delete: %d", r.Code)
	}
}

func TestResourceSaveFailuresReturnErrorAndRollback(t *testing.T) {
	for _, tc := range []struct{ name, method, path, body string }{
		{"account", "POST", "/admin/api/accounts", `{"action":"update","id":"acc-1","enabled":false}`},
		{"batch", "POST", "/admin/api/accounts", `{"action":"batch","ids":["acc-1"],"operation":"remove"}`},
		{"settings", "POST", "/admin/api/settings", `{"admin_password":"replacement"}`},
		{"key-create", "POST", "/admin/api/keys", `{"name":"unsaved"}`},
		{"group", "POST", "/admin/api/accounts", `{"action":"set-group","id":"acc-1","group":"unsaved"}`},
		{"machine", "POST", "/admin/api/accounts", `{"action":"assign-machine","id":"acc-1"}`},
		{"verified", "POST", "/admin/api/accounts", `{"action":"mark-verified","id":"acc-1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, nil)
			if err := s.Store.Save(); err != nil {
				t.Fatal(err)
			}
			// A directory at the temporary file path reliably rejects writes on all platforms.
			if err := os.Mkdir(s.Store.Path()+".tmp", 0700); err != nil {
				t.Fatal(err)
			}
			r := do(t, s, tc.method, tc.path, "", tc.body)
			if r.Code != http.StatusInternalServerError {
				t.Fatalf("expected save failure: %d %s", r.Code, r.Body.String())
			}
			if a := s.Store.AccountCopy("acc-1"); a == nil || !a.Enabled {
				t.Fatal("failed write changed account")
			}
			if a := s.Store.AccountCopy("acc-1"); a.Group != "" || a.MachineID != "" {
				t.Fatal("failed write changed account metadata")
			}
			if s.Store.AccountCopy("acc-1").EmailVerified {
				t.Fatal("failed write changed verification")
			}
			if s.Store.Settings().AdminPassword != "" || len(s.Store.Keys()) != 1 {
				t.Fatal("failed write changed settings/keys")
			}
		})
	}
}

func TestResourceMissingCheckinAndDurableClears(t *testing.T) {
	s, _ := newTestServer(t, nil)
	if r := do(t, s, "POST", "/admin/api/checkin", "", `{"id":"missing"}`); r.Code != 404 {
		t.Errorf("missing checkin: %d", r.Code)
	}
	s.Store.Record(store.LogEntry{Status: 200}, 42)
	if err := s.Store.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.Store.Path()+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/api/logs", "/admin/api/stats"} {
		r := do(t, s, "DELETE", path, "", "")
		if r.Code != 500 {
			t.Errorf("clear should report write failure: %s %d", path, r.Code)
		}
	}
	if len(s.Store.Logs(0)) != 1 || s.Store.Stats().TotalRequests != 1 {
		t.Fatal("failed clear should roll back")
	}
	if err := os.Remove(s.Store.Path() + ".tmp"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/api/logs", "/admin/api/stats"} {
		if r := do(t, s, "DELETE", path, "", ""); r.Code != 200 {
			t.Fatalf("clear: %s", r.Body.String())
		}
	}
	disk, err := store.OpenWithKey(s.Store.Path(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Logs(0)) != 0 || disk.Stats().TotalRequests != 0 {
		t.Fatal("clear did not persist")
	}
}

func TestResourceLegacyKeyReferencesAndMalformedInput(t *testing.T) {
	s, key := newTestServer(t, nil)
	r := do(t, s, "DELETE", "/admin/api/keys?toggle="+mask(key), "", "")
	if r.Code != 200 || s.Store.Keys()[0].Enabled {
		t.Fatalf("legacy toggle: %s", r.Body.String())
	}
	r = do(t, s, "DELETE", "/admin/api/keys?key="+key, "", "")
	if r.Code != 200 || len(s.Store.Keys()) != 0 {
		t.Fatalf("legacy delete: %s", r.Body.String())
	}
	for _, method := range []string{"POST", "PATCH"} {
		if r := do(t, s, method, "/admin/api/keys", "", `{`); r.Code != 400 {
			t.Fatalf("malformed %s: %d", method, r.Code)
		}
	}
	if r := do(t, s, "PATCH", "/admin/api/keys", "", `{"id":"missing","enabled":true}`); r.Code != 404 {
		t.Fatalf("missing update: %d", r.Code)
	}
}

func TestResourceEnrollmentFailurePreservesExistingVerifiedState(t *testing.T) {
	s, _ := newTestServer(t, nil)
	s.Store.UpsertAccount(&deepai.Account{ID: "acc-1", APIKey: "upstream-key", Email: "test@example.com", Enabled: true, Status: "error", Failures: 3, LastErr: "old failure", Plan: "old-plan", Theos: &deepai.Allowance{Enabled: true, Balance: "12"}})
	s.Store.SetAccountAllowance("acc-1", &deepai.Allowance{Enabled: true, Balance: "12"}, nil)
	for i := 0; i < 3; i++ {
		s.Store.MarkAccountResult("acc-1", false, "old failure")
	}
	if err := s.Store.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.Store.Path()+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.enrollRegistration(deepai.Registration{APIKey: "replacement-key", Email: "test@example.com", Plan: "new-plan", EmailVerified: true}, &deepai.Allowance{Enabled: true, Balance: "100"})
	if err == nil {
		t.Fatal("enrollment must report save failure")
	}
	a := s.Store.AccountCopy("acc-1")
	if a.APIKey != "upstream-key" || a.EmailVerified || a.Plan != "old-plan" || a.Failures != 3 || a.LastErr != "old failure" || a.Theos.Balance != "12" {
		t.Fatalf("failed enrollment changed stored account: %+v", a)
	}
	if _, _, err := s.enrollRegistration(deepai.Registration{APIKey: "brand-new", Email: "new@example.com"}, &deepai.Allowance{Enabled: true, Balance: "100"}); err == nil {
		t.Fatal("new enrollment must report save failure")
	}
	if len(s.Store.Accounts()) != 1 {
		t.Fatal("failed enrollment added an account")
	}
}
