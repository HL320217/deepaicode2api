package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/googleauth"
	"deepaicode2api/internal/store"
)

func googleRequest(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestGoogleEnrollmentUsesSavedMachineBeforeQuotaQuery(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "a@school.edu", Password: "secret"}, {Email: "b@school.edu", Password: "secret"}})
	var mu sync.Mutex
	var machines []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		machines = append(machines, r.Header.Get(deepai.HeaderMachine))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"balance":"50","granted":"50"}`))
	}))
	defer up.Close()
	s.Up.Origin = up.URL
	firstID, err := s.enrollGoogle(context.Background(), "a@school.edu", &deepai.Credentials{APIKey: "key-a"})
	if err != nil {
		t.Fatal(err)
	}
	first := s.Store.AccountCopy(firstID)
	if first.MachineID == "" {
		t.Fatal("Google enrollment lost machine identity")
	}
	if _, err := s.enrollGoogle(context.Background(), "b@school.edu", &deepai.Credentials{APIKey: "key-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.enrollGoogle(context.Background(), "a@school.edu", &deepai.Credentials{APIKey: "key-a-rotated"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(machines) != 3 || machines[0] != deepai.MachineFingerprint(first.MachineID) || machines[0] == machines[1] || machines[0] != machines[2] {
		t.Fatal("quota requests did not use stable account identities")
	}
}

func waitGoogleTask(t *testing.T, s *Server) googleTaskView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view := s.Google.snapshot()
		if view.Status == "completed" || view.Status == "cancelled" {
			return view
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Google task did not stop")
	return googleTaskView{}
}

func TestGoogleImportAPIKeepsPasswordsPrivate(t *testing.T) {
	s := newEmptyServer(t)
	w := googleRequest(t, s, http.MethodPost, "/admin/api/google/accounts", `{"content":"user@school.edu----very-private-password"}`)
	if w.Code != 200 {
		t.Fatalf("import failed: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/admin/api/google/accounts", "/admin/api/state"} {
		w = googleRequest(t, s, http.MethodGet, path, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "very-private-password") {
			t.Fatalf("invalid or leaking view %s", path)
		}
	}
	w = googleRequest(t, s, http.MethodPost, "/admin/api/google/accounts", `{"content":"invalid----very-private-password"}`)
	if w.Code != 400 || strings.Contains(w.Body.String(), "very-private-password") {
		t.Fatal("invalid import leaked credentials")
	}
}

func TestGoogleTaskOnlyEnrollsEligibleAccounts(t *testing.T) {
	s := newEmptyServer(t)
	s.quotaReprobeDelays = nil
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Api-Key"), "blocked") {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":{"code":"social_login_required","message":"Google or Apple required"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"enabled":true,"balance":"10","granted":"10"}`))
	}))
	defer up.Close()
	s.Up.Origin = up.URL
	s.googleLogin = func(ctx context.Context, credential googleauth.Credential, report func(string, string)) (*deepai.Credentials, error) {
		report("password", "正在验证密码")
		key := "good-key"
		if credential.Email == "blocked@school.edu" {
			key = "blocked-key"
		}
		return &deepai.Credentials{APIKey: key, Username: credential.Email}, nil
	}
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "good@company.org", Password: "secret-one"}, {Email: "blocked@school.edu", Password: "secret-two"}})
	w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":2}`)
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	view := waitGoogleTask(t, s)
	if view.Succeeded != 1 || view.Failed != 1 || len(s.Store.Accounts()) != 1 {
		t.Fatalf("incorrect task result: %+v", view)
	}
	a := s.Store.Accounts()[0]
	if a.Source != "google" || a.Email != "good@company.org" || a.Password != "" {
		t.Fatal("wrong source, email or duplicate Google password")
	}
	raw, _ := json.Marshal(s.Google.snapshot())
	if strings.Contains(string(raw), "secret-") || strings.Contains(string(raw), "good-key") {
		t.Fatal("task leaks credentials")
	}
	pool := s.Store.GoogleAccounts()
	if pool[0].Status != "authorized" || pool[1].Status != "failed" {
		t.Fatalf("incorrect pool statuses: %+v", pool)
	}
}

func TestGoogleTaskRecordsBrowserRejectionAndContinuesQueue(t *testing.T) {
	s := newEmptyServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"balance":"50","granted":"50"}`))
	}))
	defer up.Close()
	s.Up.Origin = up.URL
	s.googleLogin = func(ctx context.Context, c googleauth.Credential, report func(string, string)) (*deepai.Credentials, error) {
		if c.Email == "rejected@school.edu" {
			return nil, googleauth.ErrBrowserRejected
		}
		return &deepai.Credentials{APIKey: "fixture-key"}, nil
	}
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "rejected@school.edu", Password: "fixture-secret"}, {Email: "next@school.edu", Password: "fixture-secret"}})
	if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":2}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	view := waitGoogleTask(t, s)
	pool := s.Store.GoogleAccounts()
	if view.Failed != 1 || view.Succeeded != 1 || pool[0].Status != "failed" || pool[1].Status != "authorized" {
		t.Fatalf("browser rejection stalled queue or enrolled failed account: %+v", view)
	}
	if !strings.Contains(pool[0].Message, "不安全") || strings.Contains(pool[0].Message, "fixture-secret") || strings.Contains(pool[0].Message, "密码错误") {
		t.Fatalf("incorrect rejection cause: %s", pool[0].Message)
	}
	if len(s.Store.Accounts()) != 1 || s.Store.Accounts()[0].Email != "next@school.edu" {
		t.Fatal("browser-rejected account entered DeepAI account pool")
	}
}

func TestGoogleTaskCancelReleasesQueue(t *testing.T) {
	s := newEmptyServer(t)
	started := make(chan struct{})
	s.googleLogin = func(ctx context.Context, credential googleauth.Credential, report func(string, string)) (*deepai.Credentials, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "a@company.org", Password: "secret"}, {Email: "b@company.org", Password: "secret"}})
	if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":2}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	<-started
	if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":1}`); w.Code != 409 {
		t.Fatal("concurrent task accepted")
	}
	if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/cancel", `{}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	view := waitGoogleTask(t, s)
	if view.Status != "cancelled" || len(s.Store.Accounts()) != 0 {
		t.Fatal("cancelled task enrolled an account")
	}
	for _, a := range s.Store.GoogleAccounts() {
		if a.Status != "available" {
			t.Fatalf("account remained reserved: %+v", a)
		}
	}
}

func TestGoogleTaskReportsZeroQuotaAfterSuccessfulAuthorization(t *testing.T) {
	s := newEmptyServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"balance":"0.00000","granted":"0.00000"}`))
	}))
	defer up.Close()
	s.Up.Origin = up.URL
	s.googleLogin = func(context.Context, googleauth.Credential, func(string, string)) (*deepai.Credentials, error) {
		return &deepai.Credentials{APIKey: "good-key"}, nil
	}
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "a@school.edu", Password: "secret"}})
	if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":1}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	view := waitGoogleTask(t, s)
	if view.Succeeded != 1 || !strings.Contains(view.Message, "0.00000 Theos") || !strings.Contains(view.Message, "无可用额度") {
		t.Fatalf("authorization result hides zero balance: %+v", view)
	}
}

func TestGoogleStartRejectsInvalidConfigurationBeforeReservingAccounts(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "a@school.edu", Password: "secret"}})
	t.Setenv("DEEPAI_GOOGLE_HEADLESS", "true")
	t.Setenv("DEEPAI_GOOGLE_PROXY", "https://invalid.test:443")
	w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":1}`)
	if w.Code != 400 || s.Store.GoogleAccounts()[0].Status != "available" {
		t.Fatal("bad global config consumed an account")
	}
}
