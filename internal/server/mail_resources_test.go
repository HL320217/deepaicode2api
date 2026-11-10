package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/googleauth"
	"deepaicode2api/internal/store"
)

func TestMailResourcePreviewAndMaintenance(t *testing.T) {
	for _, kind := range []string{"google", "graph"} {
		t.Run(kind, func(t *testing.T) {
			s := newEmptyServer(t)
			path := "/admin/api/google/accounts"
			content := "a@example.com----private-old"
			next := "a@example.com----private-new\nb@example.com----private-new"
			update := `{"action":"update","email":"A@example.com","password":"private-edit"}`
			if kind == "graph" {
				path = "/admin/api/register/graph-mail"
				content += "----client----private-refresh"
				next = "a@example.com----private-new----client----private-refresh\nb@example.com----private-new----client----private-refresh"
				update = `{"action":"update","email":"A@example.com","password":"private-edit","client_id":"new-client","refresh_token":"private-edit-refresh"}`
			}
			post := func(content string, preview bool) string {
				raw, _ := json.Marshal(map[string]any{"content": content, "preview": preview})
				return string(raw)
			}
			if w := googleRequest(t, s, "POST", path, post(content, false)); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			w := googleRequest(t, s, "POST", path, post(next, true))
			var counts map[string]int
			_ = json.Unmarshal(w.Body.Bytes(), &counts)
			if w.Code != 200 || counts["added"] != 1 || counts["updated"] != 1 || counts["count"] != 2 {
				t.Fatalf("preview: %d %s", w.Code, w.Body.String())
			}
			if w := googleRequest(t, s, "GET", path, ""); strings.Contains(w.Body.String(), "b@example.com") {
				t.Fatal("preview mutated pool")
			}
			if w := googleRequest(t, s, "POST", path, update); w.Code != 200 || strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("update: %d %s", w.Code, w.Body.String())
			}
			if kind == "google" {
				if got := s.Store.ReserveGoogleAccounts(1, false); len(got) != 1 || got[0].Password != "private-edit" {
					t.Fatal("password not updated")
				}
				s.Store.SetGoogleAccountResult("a@example.com", "authorized", "done", "", "linked")
			} else {
				raw, _ := os.ReadFile(s.graphMailPath())
				if !strings.Contains(string(raw), "private-edit----new-client----private-edit-refresh") {
					t.Fatal("Graph credentials not updated")
				}
				if err := os.WriteFile(s.graphMailPath()+".state.json", []byte(`{"a@example.com":"registered"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.Store.UpsertAccount(&deepai.Account{ID: "linked", Email: "a@example.com", APIKey: "linked-key"})
			w = googleRequest(t, s, "POST", path, strings.ReplaceAll(update, "A@example.com", "missing@example.com"))
			if w.Code != 404 {
				t.Fatalf("missing update: %d", w.Code)
			}
			w = googleRequest(t, s, "DELETE", path, `{"emails":["A@example.com","missing@example.com"]}`)
			var result struct{ Succeeded, Failed int }
			_ = json.Unmarshal(w.Body.Bytes(), &result)
			if w.Code != 200 || result.Succeeded != 1 || result.Failed != 1 {
				t.Fatalf("delete: %d %s", w.Code, w.Body.String())
			}
			if s.Store.AccountCopy("linked") == nil {
				t.Fatal("credential deletion removed DeepAI account")
			}
			if w := googleRequest(t, s, "GET", path, ""); w.Code != 200 || strings.Contains(w.Body.String(), "a@example.com") {
				t.Fatalf("deleted mailbox visible: %d %s", w.Code, w.Body.String())
			}
			if kind == "graph" {
				raw, _ := os.ReadFile(s.graphMailPath() + ".state.json")
				if !strings.Contains(string(raw), "registered") {
					t.Fatal("deletion erased history")
				}
			}
		})
	}
}

func TestMailResourcesRejectMutationsDuringTask(t *testing.T) {
	for _, kind := range []string{"google", "graph"} {
		t.Run(kind, func(t *testing.T) {
			s := newEmptyServer(t)
			path := "/admin/api/google/accounts"
			if kind == "google" {
				s.Google.current = &googleTask{view: googleTaskView{Status: "running"}}
			} else {
				path = "/admin/api/register/graph-mail"
				s.Registers.put(&registerJob{ID: "busy", Backend: "graph", running: true})
			}
			for _, req := range []struct{ method, body string }{{"POST", `{"action":"update","email":"a@example.com","password":"new"}`}, {"DELETE", `{"emails":["a@example.com"]}`}, {"POST", `{"content":"a@example.com----secret----client----refresh"}`}} {
				if w := googleRequest(t, s, req.method, path, req.body); w.Code != 409 {
					t.Fatalf("%s not blocked: %d %s", req.method, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestGoogleMaintenanceRollsBackSaveFailure(t *testing.T) {
	for _, req := range []struct{ method, body string }{{"POST", `{"content":"b@example.com----new"}`}, {"POST", `{"action":"update","email":"a@example.com","password":"new"}`}, {"DELETE", `{"emails":["a@example.com"]}`}} {
		t.Run(req.method+req.body, func(t *testing.T) {
			s := newEmptyServer(t)
			s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "a@example.com", Password: "old"}})
			if err := os.Mkdir(s.Store.Path()+".tmp", 0700); err != nil {
				t.Fatal(err)
			}
			w := googleRequest(t, s, req.method, "/admin/api/google/accounts", req.body)
			if w.Code != 500 {
				t.Fatalf("expected failed save: %d %s", w.Code, w.Body.String())
			}
			got := s.Store.ReserveGoogleAccounts(10, false)
			if len(got) != 1 || got[0].Password != "old" {
				t.Fatal("failed persistence changed memory")
			}
		})
	}
}

func TestGoogleSelectedRetrySkipsAuthorized(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "other@example.com", Password: "x"}, {Email: "retry@example.com", Password: "x"}, {Email: "done@example.com", Password: "x"}})
	s.Store.SetGoogleAccountResult("retry@example.com", "failed", "failed", "", "")
	s.Store.SetGoogleAccountResult("done@example.com", "authorized", "done", "", "linked")
	seen := make(chan string, 3)
	s.googleLogin = func(_ context.Context, c googleauth.Credential, _ func(string, string)) (*deepai.Credentials, error) {
		seen <- c.Email
		return nil, errors.New("test login failure")
	}
	w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":3,"retry_failed":true,"emails":["retry@example.com","done@example.com"]}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	waitGoogleTask(t, s)
	if len(seen) != 1 || <-seen != "retry@example.com" {
		t.Fatal("selected retry used unselected or authorized account")
	}
}

func TestGoogleCredentialUpdateRejectsEmailDelimiter(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "a@example.com", Password: "old"}})
	w := googleRequest(t, s, "POST", "/admin/api/google/accounts", `{"action":"update","email":"a@example.com----injected","password":"new"}`)
	if w.Code != 400 {
		t.Fatalf("malformed email update accepted: %d", w.Code)
	}
	got := s.Store.ReserveGoogleAccounts(1, false)
	if len(got) != 1 || got[0].Password != "old" {
		t.Fatal("malformed email changed another account's password")
	}
}
