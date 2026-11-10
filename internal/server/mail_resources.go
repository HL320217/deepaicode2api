package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func normalizeResourceEmails(input []string) ([]string, error) {
	if len(input) == 0 || len(input) > 1000 {
		return nil, errors.New("请选择 1–1000 个邮箱")
	}
	out := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, email := range input {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || !strings.Contains(email, "@") || strings.ContainsAny(email, "\r\n\x00") {
			return nil, errors.New("邮箱格式无效")
		}
		if !seen[email] {
			out = append(out, email)
			seen[email] = true
		}
	}
	return out, nil
}

func mailboxDeleteResult(emails []string, removed map[string]bool) map[string]any {
	results := make([]map[string]any, 0, len(emails))
	succeeded := 0
	for _, email := range emails {
		item := map[string]any{"id": email, "ok": removed[email]}
		if removed[email] {
			succeeded++
		} else {
			item["error"] = "邮箱不存在"
		}
		results = append(results, item)
	}
	return map[string]any{"results": results, "succeeded": succeeded, "failed": len(results) - succeeded}
}

func graphCredentialLines(content string) []string {
	lines := []string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "\ufeff"))
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines
}

func graphCredentialEmail(line string) string {
	email, _, _ := strings.Cut(line, "----")
	return strings.ToLower(strings.TrimSpace(email))
}

// Called with graphImportMu held, as is task startup, so a new provider cannot
// retain stale credentials between this check and the atomic file replacement.
func (s *Server) graphPoolActive() bool {
	for _, job := range s.Registers.list() {
		if job.Backend == "graph" && job.isRunning() {
			return true
		}
	}
	return false
}

func writeGraphCredentials(path string, lines []string) error {
	if len(lines) == 0 {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".graph-mail-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
