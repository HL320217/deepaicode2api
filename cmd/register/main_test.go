package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"deepaicode2api/internal/register"
)

func TestFailedCLIExitFlushesRegisteredAccount(t *testing.T) {
	if os.Getenv("DEEPAI_REGISTER_TEST_CHILD") == "1" {
		os.Args = []string{"register", "-resume", "-no-enroll", "-verify=false", "-verify-grace=0", "-delay=0", "-ledger", os.Getenv("DEEPAI_REGISTER_TEST_LEDGER")}
		main()
		return
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	ledger := register.NewLedger(path)
	if err := ledger.Append(&register.Record{Email: "a@example.com", Status: register.StatusFailed, APIKey: "upstream-key", SessionKey: "session-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	mailPath := filepath.Join(dir, "mail.txt")
	if err := os.WriteFile(mailPath, []byte("a@example.com----pw----client----refresh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"social_login_required","message":"Google or Apple required"}}`))
	}))
	defer srv.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFailedCLIExitFlushesRegisteredAccount$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DEEPAI_REGISTER_TEST_CHILD=1", "DEEPAI_REGISTER_TEST_LEDGER="+path,
		"DEEPAI_GRAPH_MAIL_FILE="+mailPath, "DEEPAI_API_ORIGIN="+srv.URL, "DEEPAI_MACHINE_ID=test-machine")
	output, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected failed exit for unusable key, got %v: %s", err, output)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var last register.Record
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Status != register.StatusRegistered || last.ErrorCode != "social_login_required" {
		t.Fatalf("failed exit lost buffered registration result: status=%s error_code=%s; output=%s", last.Status, last.ErrorCode, output)
	}
	records, err := ledger.Load()
	if err != nil || len(records) != 1 || records[0].SessionKey != "session-secret" {
		t.Fatalf("registered credentials were lost: count=%d, err=%v", len(records), err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
}
