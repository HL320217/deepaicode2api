package register

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"deepaicode2api/internal/deepai"
)

func TestSocialLoginRequirementPreservesRegisteredAccount(t *testing.T) {
	rec := NewRecord("registered@example.com")
	rec.Status = StatusRegistered
	rec.APIKey = "upstream-key"
	rec.Mark("register", "ok", 0)
	rec.Mark("validate", "failed", 0)
	err := fmt.Errorf("Theos 验活失败: %w", &deepai.HTTPError{
		Op: "查询 Theos", Status: http.StatusForbidden,
		Body: []byte(`{"error":{"code":"social_login_required","message":"Sign in with Google or Apple"}}`),
	})
	rec.SetError("validate", err)
	if rec.Status != StatusRegistered || rec.APIKey != "upstream-key" {
		t.Fatalf("lost completed registration: status=%s key_present=%v", rec.Status, rec.APIKey != "")
	}
	if rec.Stages["validate"] != "blocked" || !strings.Contains(rec.Error, "Google") || !strings.Contains(rec.Error, "账号已注册") {
		t.Fatalf("expected an explicit Code authorization block, got %s / %s", rec.Stages["validate"], rec.Error)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["error_code"] != "social_login_required" {
		t.Fatalf("missing upstream error code: %v", payload["error_code"])
	}
	if rec.WorthDelivering() {
		t.Fatal("blocked account cannot be delivered as a usable Code key")
	}
}

func TestDeliverablesExcludeUnvalidatedKeys(t *testing.T) {
	registered := &Record{Email: "registered@example.com", APIKey: "blocked-key", Status: StatusRegistered}
	failed := &Record{Email: "failed@example.com", APIKey: "failed-key", Status: StatusFailed}
	usable := &Record{Email: "usable@example.com", APIKey: "usable-key", Status: StatusEnrolled}
	out, err := WriteDeliverables(t.TempDir(), []*Record{registered, failed, usable})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{out.KeysTxt, out.APIKeysTxt, out.SuccessJSON} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "blocked-key") || strings.Contains(string(raw), "failed-key") {
			t.Fatalf("unusable keys were exported as successful: %s", path)
		}
		if !strings.Contains(string(raw), "usable-key") {
			t.Fatalf("usable key missing from %s", path)
		}
	}
}
