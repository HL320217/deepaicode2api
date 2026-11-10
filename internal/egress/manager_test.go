package egress

import (
	"context"
	"deepaicode2api/internal/deepai"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRotationWaitsForManagedResponseBody(t *testing.T) {
	rotations := make(chan struct{}, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "response") }))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ip":"203.0.113.1"}`) }))
	defer proxy.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rotations <- struct{}{}; fmt.Fprint(w, "ok") }))
	defer provider.Close()
	m := New(Config{Enabled: true, ProxyURL: proxy.URL, RefreshURL: provider.URL, IPURL: "http://ip.invalid", Threshold: 5, VerifyTimeoutSec: 1}, State{}, nil)
	client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	m.Guard(client)
	response, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err = m.Action(ctx, "rotate", deepai.NewClientWithProxy(proxy.URL)); err == nil {
		t.Fatal("rotation while body active")
	}
	select {
	case <-rotations:
		t.Fatal("provider called before body closed")
	default:
	}
	_ = response.Body.Close()
	if err = m.Action(context.Background(), "rotate", deepai.NewClientWithProxy(proxy.URL)); err == nil {
		t.Fatal("unchanged IP accepted")
	}
	select {
	case <-rotations:
	default:
		t.Fatal("provider never called after body closed")
	}
}

func TestZeroAndFailedQueriesNeverCountAsGrants(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "ip.invalid" {
			fmt.Fprint(w, `{"ip":"203.0.113.1"}`)
			return
		}
		if r.Header.Get("Api-Key") == "invalid" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"code":"invalid_api_key"}}`)
			return
		}
		fmt.Fprint(w, `{"balance":"0","granted":"0"}`)
	}))
	defer proxy.Close()
	m := New(Config{Enabled: true, ProxyURL: proxy.URL, IPURL: "http://ip.invalid", Threshold: 5}, State{}, nil)
	up := deepai.NewClientWithProxy(proxy.URL)
	up.Origin = "http://up.invalid"
	for _, key := range []string{"zero", "invalid"} {
		_, _ = m.Query(context.Background(), up, &deepai.Account{APIKey: key, MachineID: "stable"})
	}
	if m.Status().Count != 0 {
		t.Fatal("failed/zero request counted")
	}
}

func TestAmbiguousGrantIsReservedAndRecoveredAfterRestart(t *testing.T) {
	var granted bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "ip.invalid" {
			fmt.Fprint(w, `{"ip":"203.0.113.1"}`)
			return
		}
		if r.URL.Path == "/deepai_code/models" {
			fmt.Fprint(w, `{"models":[{"id":"test"}]}`)
			return
		}
		if !granted && r.Header.Get("X-DeepAI-Code-Machine") == "" {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":{"code":"machine_required"}}`)
			return
		}
		if !granted {
			granted = true
			w.WriteHeader(502)
			fmt.Fprint(w, `{"error":{"code":"gateway_error"}}`)
			return
		}
		fmt.Fprint(w, `{"balance":"50","granted":"50","refresh_at":"2099-01-01T00:00:00Z"}`)
	}))
	defer proxy.Close()
	c := Config{Enabled: true, ProxyURL: proxy.URL, IPURL: "http://ip.invalid", Threshold: 5}
	var saved State
	save := func(_ Config, s State) error { saved = CloneState(s); return nil }
	m := New(c, State{}, save)
	up := deepai.NewClientWithProxy(proxy.URL)
	up.Origin = "http://up.invalid"
	acct := &deepai.Account{APIKey: "key", MachineID: "fixed"}
	if _, err := m.Query(context.Background(), up, acct); err == nil {
		t.Fatal("ambiguous 502 accepted")
	}
	if !m.Status().Paused {
		t.Fatal("ambiguous grant must pause")
	}
	m = New(c, saved, save)
	if err := m.Action(context.Background(), "resume", up); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Query(context.Background(), up, acct); err != nil {
		t.Fatal(err)
	}
	if m.Status().Count != 1 {
		t.Fatalf("lost interrupted grant: %+v", m.Status())
	}
}

func TestExpiredPendingDoesNotAttributeExistingBalanceToOldExit(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"balance":"50","refresh_at":"2099-01-01T00:00:00Z"}`)
	}))
	defer proxy.Close()
	acct := &deepai.Account{APIKey: "existing-key", MachineID: "fixed"}
	m := New(Config{Enabled: true, ProxyURL: proxy.URL, Threshold: 5}, State{IP: "203.0.113.1", Pending: map[string]Receipt{receiptKey(acct): {IP: "203.0.113.1", Until: time.Now().Add(-time.Hour).Unix()}}}, nil)
	up := deepai.NewClientWithProxy(proxy.URL)
	up.Origin = "http://up.invalid"
	if _, err := m.Query(context.Background(), up, acct); err != nil {
		t.Fatal(err)
	}
	if m.Status().Count != 0 {
		t.Fatal("expired pending revived as new receipt")
	}
}

// All traffic, including the IP lookup, must pass through the configured proxy.
func TestRotationCountsNewGrantsNotExistingOrRepeatedBalances(t *testing.T) {
	var mu sync.Mutex
	ip := "203.0.113.1"
	rotations := 0
	balances := map[string]bool{"existing": true}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Host == "ip.invalid" {
			fmt.Fprintf(w, `{"ip":%q}`, ip)
			return
		}
		if r.URL.Path == "/deepai_code/models" {
			fmt.Fprint(w, `{"models":[{"id":"test"}]}`)
			return
		}
		key := r.Header.Get("Api-Key")
		if !balances[key] && r.Header.Get("X-DeepAI-Code-Machine") == "" {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":{"code":"machine_required"}}`)
			return
		}
		balances[key] = true
		fmt.Fprintf(w, `{"balance":"50","granted":"50","refresh_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer proxy.Close()
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Api-Key") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("credentials sent to provider")
		}
		rotations++
		ip = "203.0.113.2"
		fmt.Fprint(w, "ok")
	}))
	defer refresh.Close()
	cfg := Config{Enabled: true, ProxyURL: proxy.URL, RefreshURL: refresh.URL, IPURL: "http://ip.invalid", AutoRotate: true, Threshold: 2, VerifyTimeoutSec: 1}
	var saved State
	persist := func(_ Config, s State) error { saved = CloneState(s); return nil }
	m := New(cfg, State{}, persist)
	up := deepai.NewClientWithProxy(proxy.URL)
	up.Origin = "http://up.invalid"
	query := func(key string) {
		t.Helper()
		a := &deepai.Account{APIKey: key, MachineID: "stable-" + key}
		if _, err := m.Query(context.Background(), up, a); err != nil {
			t.Fatal(err)
		}
	}
	query("existing")
	query("a")
	query("a")
	query("b")
	if rotations != 0 || m.Status().Count != 2 {
		t.Fatalf("premature rotation/count: %d %+v", rotations, m.Status())
	}
	// Restart must not clear the counter or duplicate receipts.
	m = New(cfg, saved, persist)
	query("b")
	// Registration rotates before starting a new account workflow, not midway
	// through an already-authenticated web session.
	ctx, _, release, err := m.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.PrepareRegistration(ctx, up); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if rotations != 1 {
		t.Fatal("registration did not rotate at its boundary")
	}
	query("c")
	if rotations != 1 || m.Status().IP != "203.0.113.2" || m.Status().Count != 1 {
		t.Fatalf("rotation after restart: %d %+v", rotations, m.Status())
	}
}

func TestUnchangedIPPausesAndDoesNotRepeatProviderGET(t *testing.T) {
	rotations := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ip":"203.0.113.1"}`) }))
	defer proxy.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rotations++; fmt.Fprint(w, "ok") }))
	defer provider.Close()
	m := New(Config{Enabled: true, ProxyURL: proxy.URL, RefreshURL: provider.URL, IPURL: "http://ip.invalid", Threshold: 5, VerifyTimeoutSec: 1}, State{}, nil)
	up := deepai.NewClientWithProxy(proxy.URL)
	if err := m.Action(context.Background(), "rotate", up); err == nil {
		t.Fatal("unchanged IP accepted")
	}
	if !m.Status().Paused || rotations != 1 {
		t.Fatalf("not paused or repeated GET: %d %+v", rotations, m.Status())
	}
	if _, _, release, err := m.Session(context.Background()); err == nil {
		release()
		t.Fatal("paused session accepted")
	}
}

func TestCancelledWaitDoesNotEnterSession(t *testing.T) {
	m := New(Config{Enabled: true, ProxyURL: "http://127.0.0.1:1", Threshold: 5}, State{}, nil)
	release, err := m.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = m.acquire(ctx); err == nil {
		t.Fatal("cancelled wait acquired lease")
	}
}

func TestPauseTakesEffectWhileSessionIsInFlight(t *testing.T) {
	m := New(Config{Enabled: true, ProxyURL: "http://127.0.0.1:1", Threshold: 5}, State{}, nil)
	_, _, release, err := m.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err = m.Action(ctx, "pause", nil); err != nil {
		t.Fatalf("pause waited for active work: %v", err)
	}
	if !m.Status().Paused {
		t.Fatal("pause not recorded")
	}
}
