package register

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedProxyCredentialsStayOutOfLedgerAndDelivery(t *testing.T) {
	rec := &Record{Email: "fixture@example.invalid", Status: StatusEnrolled, APIKey: "fixture-key", Proxy: "http://user:proxy-secret@localhost:1234", ManagedProxy: true}
	ledger := NewLedger(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err := ledger.Append(rec); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(ledger.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "proxy-secret") {
		t.Fatal("dynamic proxy credential in ledger")
	}
	if redactRecord(rec).Proxy != "" {
		t.Fatal("dynamic proxy credential in delivery")
	}
	if rec.Proxy != "http://user:proxy-secret@localhost:1234" {
		t.Fatal("in-memory proxy needed for enrollment was lost")
	}
}
