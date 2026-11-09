package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	s.quotaReprobeDelays = nil
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

func TestGoogleEnrollmentRechecksQuota(t *testing.T) {
	for _, tc := range []struct {
		name     string
		balances []string
	}{
		{"delayed-grant", []string{"0", "200"}},
		{"still-zero", []string{"0", "0", "0"}},
		{"already-positive", []string{"50"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newEmptyServer(t)
			s.quotaReprobeDelays = []time.Duration{30 * time.Millisecond, 30 * time.Millisecond}
			const machine = "b8d56b1e-5fc2-493d-a8e2-c4b447e54e7c"
			s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "quota@school.edu", Password: "fixture-secret", MachineID: machine}})
			var mu sync.Mutex
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/deepai_code/theos" || r.Header.Get(deepai.HeaderMachine) != deepai.MachineFingerprint(machine) {
					t.Error("quota recheck changed the saved identity or endpoint")
				}
				mu.Lock()
				i := calls
				calls++
				mu.Unlock()
				if i >= len(tc.balances) {
					i = len(tc.balances) - 1
				}
				_, _ = fmt.Fprintf(w, `{"enabled":true,"balance":%q,"granted":%q}`, tc.balances[i], tc.balances[i])
			}))
			defer up.Close()
			s.Up.Origin = up.URL
			s.googleLogin = func(context.Context, googleauth.Credential, func(string, string)) (*deepai.Credentials, error) {
				return &deepai.Credentials{APIKey: "fixture-key"}, nil
			}
			if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":1}`); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if view := waitGoogleTask(t, s); view.Succeeded != 1 {
				t.Fatalf("Google enrollment failed: %+v", view)
			}
			id := s.Store.GoogleAccounts()[0].AccountID

			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				mu.Lock()
				n := calls
				mu.Unlock()
				a := s.Store.AccountCopy(id)
				if n >= len(tc.balances) && a.Theos.Balance == tc.balances[len(tc.balances)-1] {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			// Allow any unnecessary extra retries to become observable too.
			time.Sleep(120 * time.Millisecond)
			mu.Lock()
			n := calls
			mu.Unlock()
			if n != len(tc.balances) {
				t.Fatalf("quota queries = %d, want %d", n, len(tc.balances))
			}
			want := tc.balances[len(tc.balances)-1]
			a := s.Store.AccountCopy(id)
			if a.Theos.Balance != want || a.MachineID != machine {
				t.Fatalf("quota recheck did not retain expected balance %s and saved identity", want)
			}
			w := googleRequest(t, s, http.MethodGet, "/admin/api/google/accounts", "")
			if w.Code != 200 || !strings.Contains(w.Body.String(), want+" Theos") {
				t.Fatalf("Google view did not reflect latest quota: %s", w.Body.String())
			}
			if tc.name == "delayed-grant" {
				persisted, err := store.OpenWithKey(s.Store.Path(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if got := persisted.AccountCopy(id); got == nil || got.Theos.Balance != "200" {
					t.Fatal("delayed quota result was not saved")
				}
			}
		})
	}
}

func TestGooglePoolReflectsManualQuotaRefresh(t *testing.T) {
	s := newEmptyServer(t)
	s.quotaReprobeDelays = nil
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "quota@school.edu", Password: "fixture-secret"}})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"enabled":true,"balance":"200","granted":"200"}`))
	}))
	defer up.Close()
	s.Up.Origin = up.URL
	s.Store.UpsertAccount(&deepai.Account{ID: "quota", APIKey: "fixture-key", Theos: &deepai.Allowance{Enabled: true, Balance: "0"}})
	s.Store.SetGoogleAccountResult("quota@school.edu", "authorized", "done", "授权完成；当前余额 0 Theos（当前无可用额度）", "quota")
	if w := googleRequest(t, s, http.MethodPost, "/admin/api/checkin", `{"id":"quota"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := googleRequest(t, s, http.MethodGet, "/admin/api/google/accounts", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "200 Theos") || strings.Contains(w.Body.String(), "无可用额度") {
		t.Fatalf("Google view kept stale authorization balance after refresh: %s", w.Body.String())
	}
}

func TestGoogleQuotaViewPreservesAuthorizationState(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.UpsertAccount(&deepai.Account{ID: "linked", APIKey: "private-key", Theos: &deepai.Allowance{Enabled: true, Balance: "200"}})
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "quota@school.edu", Password: "private-password"}})
	readView := func() store.GoogleAccountView {
		t.Helper()
		w := googleRequest(t, s, http.MethodGet, "/admin/api/google/accounts", "")
		var body struct {
			Accounts []store.GoogleAccountView `json:"accounts"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || len(body.Accounts) != 1 {
			t.Fatalf("invalid pool response: %s", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-") {
			t.Fatal("quota view leaked credentials")
		}
		return body.Accounts[0]
	}
	for _, status := range []string{"available", "queued", "running", "action_required", "failed"} {
		s.Store.SetGoogleAccountResult("quota@school.edu", status, "original", "original message", "linked")
		v := readView()
		if v.Status != status || v.Phase != "original" || v.Message != "original message" {
			t.Fatalf("quota display replaced active or failed login state: %+v", v)
		}
	}
	s.Store.SetGoogleAccountResult("quota@school.edu", "authorized", "done", "old quota", "linked")
	s.Store.SetAccountAllowance("linked", nil, errors.New("upstream error with private-key"))
	v := readView()
	if v.Status != "authorized" || v.Phase != "done" || !strings.Contains(v.Message, "状态异常") || strings.Contains(v.Message, "200 Theos") {
		t.Fatalf("failed refresh presented cached balance as current: %+v", v)
	}
	s.Store.SetAccountAllowance("linked", &deepai.Allowance{Enabled: true, Balance: "25"}, nil)
	if v := readView(); !strings.Contains(v.Message, "25 Theos") || strings.Contains(v.Message, "状态异常") {
		t.Fatalf("successful refresh did not clear quota error: %+v", v)
	}
	if v := s.Store.GoogleAccounts()[0]; v.Message != "old quota" {
		t.Fatal("quota view overwrote the original authorization result")
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

func TestGoogleTaskAutomaticallySkipsStalledLoginAndContinues(t *testing.T) {
	for _, mode := range []string{"manual", "repeated-progress", "silent"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DEEPAI_GOOGLE_IDLE_TIMEOUT", "250ms")
			s := newEmptyServer(t)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"enabled":true,"balance":"50","granted":"50"}`))
			}))
			defer up.Close()
			s.Up.Origin = up.URL
			s.googleLogin = func(ctx context.Context, c googleauth.Credential, report func(string, string)) (*deepai.Credentials, error) {
				if c.Email == "next@school.edu" {
					return &deepai.Credentials{APIKey: "next-fixture-key"}, nil
				}
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for n := 0; ; n++ {
					switch mode {
					case "manual":
						report("action_required", fmt.Sprintf("等待人工确认 %d", n))
					case "repeated-progress":
						report("consent", "正在确认 DeepAI 的 Google 授权")
					}
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-ticker.C:
					}
				}
			}
			s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "stalled@school.edu", Password: "private-password"}, {Email: "next@school.edu", Password: "private-password"}})
			if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":2}`); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			t.Cleanup(func() {
				s.Google.current.cancel()
				waitGoogleTask(t, s)
			})
			view := waitGoogleTask(t, s)
			pool := s.Store.GoogleAccounts()
			if view.Skipped != 1 || view.Failed != 0 || view.Succeeded != 1 || pool[0].Phase != "skipped" || pool[0].Status != "failed" || pool[1].Status != "authorized" {
				t.Fatalf("stalled account did not auto-skip and continue: %+v; pool: %+v", view, pool)
			}
			if !strings.Contains(pool[0].Message, "自动跳过") || strings.Contains(pool[0].Message, "private-password") {
				t.Fatalf("missing safe auto-skip explanation: %s", pool[0].Message)
			}
			if got := s.Store.ReserveGoogleAccounts(1, true); len(got) != 1 || got[0].Email != "stalled@school.edu" {
				t.Fatal("automatically skipped account cannot be retried")
			}
		})
	}
}

func TestGoogleTaskResetsIdleTimeoutOnNewProgress(t *testing.T) {
	t.Setenv("DEEPAI_GOOGLE_IDLE_TIMEOUT", "250ms")
	s := newEmptyServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Enrollment uses the account deadline, not the stopped login watchdog.
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"enabled":true,"balance":"50","granted":"50"}`))
	}))
	defer up.Close()
	s.Up.Origin = up.URL
	s.googleLogin = func(ctx context.Context, c googleauth.Credential, report func(string, string)) (*deepai.Credentials, error) {
		for _, phase := range []string{"browser", "google", "email", "password", "onboarding", "consent"} {
			report(phase, "新的授权进展")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(60 * time.Millisecond):
			}
		}
		return &deepai.Credentials{APIKey: "progress-fixture-key"}, nil
	}
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "progress@school.edu", Password: "fixture-password"}})
	if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":1}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if view := waitGoogleTask(t, s); view.Succeeded != 1 || view.Skipped != 0 {
		t.Fatalf("normal progress was timed out: %+v", view)
	}
}

func TestGoogleIdleTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{{"", time.Minute}, {"90s", 90 * time.Second}, {"2m", 2 * time.Minute}} {
		t.Run("valid/"+tc.raw, func(t *testing.T) {
			t.Setenv("DEEPAI_GOOGLE_IDLE_TIMEOUT", tc.raw)
			if got, err := googleLoginIdleTimeout(); err != nil || got != tc.want {
				t.Fatalf("timeout=%v, err=%v; want %v", got, err, tc.want)
			}
		})
	}
	for _, raw := range []string{"0s", "-1s", "invalid", "11m"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("DEEPAI_GOOGLE_IDLE_TIMEOUT", raw)
			s := newEmptyServer(t)
			s.googleLogin = func(context.Context, googleauth.Credential, func(string, string)) (*deepai.Credentials, error) {
				return nil, context.Canceled
			}
			s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "a@school.edu", Password: "fixture-password"}})
			w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":1}`)
			if w.Code != 400 || s.Store.GoogleAccounts()[0].Status != "available" {
				t.Fatalf("invalid timeout consumed an account: %d", w.Code)
			}
		})
	}
}

func TestGoogleTaskManualSkipStillContinuesQueue(t *testing.T) {
	t.Setenv("DEEPAI_GOOGLE_IDLE_TIMEOUT", "2s")
	s := newEmptyServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"enabled":true,"balance":"50","granted":"50"}`))
	}))
	defer up.Close()
	s.Up.Origin = up.URL
	started := make(chan struct{})
	s.googleLogin = func(ctx context.Context, c googleauth.Credential, report func(string, string)) (*deepai.Credentials, error) {
		if c.Email == "next@school.edu" {
			return &deepai.Credentials{APIKey: "next-fixture-key"}, nil
		}
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s.Store.MergeGoogleAccounts([]store.GoogleAccount{{Email: "skip@school.edu", Password: "fixture-password"}, {Email: "next@school.edu", Password: "fixture-password"}})
	if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/start", `{"count":2}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	<-started
	if w := googleRequest(t, s, http.MethodPost, "/admin/api/google/skip", `{}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	view := waitGoogleTask(t, s)
	if view.Skipped != 1 || view.Failed != 0 || view.Succeeded != 1 || strings.Contains(s.Store.GoogleAccounts()[0].Message, "自动跳过") {
		t.Fatalf("manual skip was lost or misclassified: %+v", view)
	}
}

func TestGoogleLoginIdleTimeoutPreservesParentDeadline(t *testing.T) {
	s := newEmptyServer(t)
	s.googleLogin = func(ctx context.Context, _ googleauth.Credential, _ func(string, string)) (*deepai.Credentials, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := s.runGoogleLogin(ctx, time.Second, googleauth.Credential{}, func(string, string) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("account deadline was misclassified as an automatic skip: %v", err)
	}
}
