package googleauth

import (
	"strings"
	"testing"
)

func TestParseAccounts(t *testing.T) {
	accounts, err := ParseAccounts("\ufeffUser@school.edu----pw----suffix\r\n# comment\nother@company.org | second\nthird@gmail.com\tthird-password\nUSER@school.edu----latest\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 3 || accounts[0].Email != "user@school.edu" || accounts[0].Password != "latest" || accounts[1].Password != "second" {
		t.Fatalf("incorrect merge: count=%d", len(accounts))
	}
}

func TestParseAccountsDoesNotExposePasswordsInErrors(t *testing.T) {
	for _, content := range []string{"invalid----secret-password", "x@example.com----", "Name <x@example.com>----secret-password", "x@example.com", "x@example.com----secret-password\x00"} {
		_, err := ParseAccounts(content)
		if err == nil || strings.Contains(err.Error(), "secret-password") {
			t.Fatalf("invalid input accepted or leaked: %v", err)
		}
	}
}

func TestParseAccountsPreservesMixedPasswordDelimiters(t *testing.T) {
	for _, line := range []string{"user@school.edu|password----suffix", "user@school.edu\tpassword|suffix----end"} {
		accounts, err := ParseAccounts(line)
		if err != nil || len(accounts) != 1 || !strings.Contains(accounts[0].Password, "----") {
			t.Fatalf("password delimiter misparsed: %v", err)
		}
	}
}
