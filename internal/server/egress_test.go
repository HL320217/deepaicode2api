package server

import (
	"context"
	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/store"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDynamicProxyConfigRequiresAdminAndHidesSecrets(t *testing.T) {
	s, _ := newTestServer(t, nil)
	s.Store.UpdateSettings(func(st *store.Settings) { st.AdminPassword = "admin-test" })
	if r := do(t, s, "GET", "/admin/api/egress", "", ""); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", r.Code)
	}
	body := `{"enabled":false,"proxy_url":"http://user:secret@127.0.0.1:8080","refresh_url":"https://provider.invalid/rotate?token=private","auto_rotate":true,"threshold":5,"ip_url":"https://api.ipify.org?format=json","verify_timeout_sec":20}`
	r := do(t, s, "POST", "/admin/api/egress", "admin-test", body)
	if r.Code != 200 {
		t.Fatalf("save: %d %s", r.Code, r.Body.String())
	}
	for _, path := range []string{"/admin/api/egress", "/admin/api/state"} {
		r = do(t, s, "GET", path, "admin-test", "")
		if r.Code != 200 || strings.Contains(r.Body.String(), "secret@") || strings.Contains(r.Body.String(), "token=private") {
			t.Fatalf("secret response %s: %d %s", path, r.Code, r.Body.String())
		}
	}
}

func TestManagedQuotaUsesConfiguredProxyAndPreservesMachine(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	granted := false
	machine := ""
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Host == "ip.invalid" {
			fmt.Fprint(w, `{"ip":"203.0.113.7"}`)
			return
		}
		requests++
		if !granted && r.Header.Get("X-DeepAI-Code-Machine") == "" {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":{"code":"machine_required"}}`)
			return
		}
		if r.Header.Get("X-DeepAI-Code-Machine") != "" {
			machine = r.Header.Get("X-DeepAI-Code-Machine")
			granted = true
		}
		fmt.Fprintf(w, `{"balance":"50","granted":"50","refresh_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer proxy.Close()
	s, _ := newTestServer(t, nil)
	s.Up.Origin = "http://unreachable.invalid"
	body := fmt.Sprintf(`{"enabled":true,"proxy_url":%q,"ip_url":"http://ip.invalid","threshold":5,"verify_timeout_sec":2}`, proxy.URL)
	if r := do(t, s, "POST", "/admin/api/egress", "", body); r.Code != 200 {
		t.Fatalf("save %d %s", r.Code, r.Body.String())
	}
	a := s.Store.AccountCopy("acc-1")
	a.MachineID = "fixed-account-machine"
	s.Store.UpsertAccount(a)
	for i := 0; i < 2; i++ {
		if r := do(t, s, "POST", "/admin/api/checkin", "", `{"id":"acc-1"}`); r.Code != 200 {
			t.Fatalf("checkin %d %s", r.Code, r.Body.String())
		}
	}
	if machine != deepai.MachineFingerprint("fixed-account-machine") || s.Egress.Status().Count != 1 {
		t.Fatalf("identity or duplicate count: %s %+v", machine, s.Egress.Status())
	}
	if requests != 3 {
		t.Fatalf("expected preflight+claim+existing lookup, got %d", requests)
	}
	// Store-backed restart retains configuration and accounting.
	reopened, err := store.Open(s.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(reopened)
	if restarted.Egress.Config().ProxyURL != proxy.URL || restarted.Egress.Status().Count != 1 {
		t.Fatal("restart lost proxy state")
	}
	if err := s.Egress.Action(context.Background(), "pause", s.managedClient(s.Up)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.queryQuota(context.Background(), a); err == nil {
		t.Fatal("paused query accepted")
	}
}
