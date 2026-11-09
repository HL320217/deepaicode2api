package register

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func graphPreflightProvider(t *testing.T, tokenStatus, mailStatus int) *graphProvider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mail.txt")
	if err := os.WriteFile(path, []byte("a@example.com----pw----client----refresh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := newGraphProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	p.client = &http.Client{Transport: graphRoundTrip(func(r *http.Request) (*http.Response, error) {
		status, body := tokenStatus, `{"access_token":"access","expires_in":3600}`
		if r.URL.Host == "login.microsoftonline.com" {
			if tokenStatus != http.StatusOK {
				body = `{"error":"invalid_grant","error_description":"secret-refresh-token"}`
			}
		} else {
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Error("missing mailbox bearer token")
			}
			if r.URL.Query().Get("$top") != "1" || r.URL.Query().Get("$select") != "id" {
				t.Error("preflight should only check read access, without fetching message bodies")
			}
			status, body = mailStatus, `{"value":[]}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return p
}

func TestGraphInboxHealthChecksMailReadPermission(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		tokenStatus, mailStatus int
		wantError               bool
	}{
		{"invalid_grant", 400, 200, true},
		{"missing_mail_read", 200, 403, true},
		{"readable_empty_inbox", 200, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := graphPreflightProvider(t, tc.tokenStatus, tc.mailStatus)
			box, err := p.Create(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			checker, ok := box.(HealthProvider)
			if !ok {
				t.Fatal("Graph inbox must check token and Mail.Read permission before registration")
			}
			err = checker.Health(context.Background())
			if (err != nil) != tc.wantError {
				t.Fatalf("health error = %v, want error = %v", err, tc.wantError)
			}
			if err != nil && (!strings.Contains(err.Error(), "Mail.Read") || strings.Contains(err.Error(), "secret-refresh-token")) {
				t.Fatalf("expected a helpful, redacted Mail.Read error: %v", err)
			}
		})
	}
}

func TestPipelineRejectsUnreadableMailboxBeforeRegistration(t *testing.T) {
	p := graphPreflightProvider(t, http.StatusBadRequest, http.StatusOK)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var upstreamCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		cancel()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	pipe := New(Options{
		Config: &Config{Proxy: proxy.URL}, Provider: p, Verify: true, NoFingerprint: true,
	})
	rec := pipe.RunOne(ctx, "", "")
	if rec.Stage != "mailbox_check" || rec.Status != StatusFailed {
		t.Fatalf("expected failure before account creation, got status=%s stage=%s", rec.Status, rec.Stage)
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("unreadable mailbox triggered %d upstream registration requests", got)
	}
	if !strings.Contains(rec.Error, "Mail.Read") {
		t.Fatalf("missing mailbox authorization guidance: %s", rec.Error)
	}
	statuses, err := p.Statuses()
	if err != nil || statuses[0].Status != "available" {
		t.Fatalf("preflight failure consumed mailbox: %v, %v", statuses, err)
	}
}

func TestGraphReleaseDoesNotReuseRegisteredMailbox(t *testing.T) {
	p := graphPreflightProvider(t, 200, 200)
	box, err := p.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := box.(*graphInbox).MarkRegistered(); err != nil {
		t.Fatal(err)
	}
	releaser, ok := box.(interface{ Release() error })
	if !ok {
		t.Fatal("Graph inbox must release a reservation after preflight failure")
	}
	if err := releaser.Release(); err != nil {
		t.Fatal(err)
	}
	statuses, err := p.Statuses()
	if err != nil || statuses[0].Status != "registered" {
		t.Fatalf("release made an existing account reusable: %v, %v", statuses, err)
	}
}

func TestGraphReleaseSkipsUnreadableMailboxInCurrentBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.txt")
	if err := os.WriteFile(path, []byte("bad@example.com----pw----client----refresh\ngood@example.com----pw----client----refresh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := newGraphProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	box, err := p.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := box.(*graphInbox).Release(); err != nil {
		t.Fatal(err)
	}
	next, err := p.Create(context.Background())
	if err != nil || next.Email() != "good@example.com" {
		t.Fatalf("batch reused unreadable mailbox instead of trying the next: %v, %v", next, err)
	}
	statuses, err := p.Statuses()
	if err != nil || statuses[0].Status != "available" {
		t.Fatalf("unregistered mailbox should remain available for updated credentials: %v, %v", statuses, err)
	}
}
