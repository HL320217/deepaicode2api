package store

import (
	"errors"
	"strings"
	"time"

	"deepaicode2api/internal/deepai"
)

// GoogleAccount is private persistent state, covered by the store master key.
// Admin responses use GoogleAccountView and never serialize this type.
type GoogleAccount struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	Status    string `json:"status"`
	Phase     string `json:"phase,omitempty"`
	Message   string `json:"message,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	MachineID string `json:"machine_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

// EnsureGoogleMachineID reuses an enrolled account's identity, or allocates one
// once in the private Google pool. Call Save before making the first quota query
// so retries and interrupted logins cannot silently rotate the identity.
func (s *Store) EnsureGoogleMachineID(email, apiKey string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.GoogleAccounts {
		if !strings.EqualFold(strings.TrimSpace(email), a.Email) {
			continue
		}
		for _, existing := range s.st.Accounts {
			if existing.APIKey != apiKey && !sameEmail(existing.Email, email) {
				continue
			}
			if id := strings.TrimSpace(existing.MachineID); id != "" {
				a.MachineID = id
				break
			}
		}
		if a.MachineID == "" {
			a.MachineID = deepai.NewMachineUUID()
		}
		if a.MachineID == "" {
			return "", errors.New("无法生成 Google 账号设备标识")
		}
		return a.MachineID, nil
	}
	return "", errors.New("Google 账号不在号池中")
}

type GoogleAccountView struct {
	Email     string `json:"email"`
	Status    string `json:"status"`
	Phase     string `json:"phase,omitempty"`
	Message   string `json:"message,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Store) GoogleAccounts() []GoogleAccountView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]GoogleAccountView, 0, len(s.st.GoogleAccounts))
	for _, a := range s.st.GoogleAccounts {
		if a.Password == "" {
			continue
		}
		out = append(out, GoogleAccountView{a.Email, a.Status, a.Phase, a.Message, a.AccountID, a.UpdatedAt})
	}
	return out
}

// MergeGoogleAccounts updates credentials without resetting completed results.
func (s *Store) MergeGoogleAccounts(accounts []GoogleAccount) (added, updated int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mergeGoogleAccountsLocked(accounts)
}

func (s *Store) mergeGoogleAccountsLocked(accounts []GoogleAccount) (added, updated int) {
	for _, input := range accounts {
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		found := false
		for _, old := range s.st.GoogleAccounts {
			if old.Email == input.Email {
				if old.Password == "" {
					added++
				} else {
					updated++
				}
				old.Password = input.Password
				old.UpdatedAt = time.Now().Unix()
				found = true
				break
			}
		}
		if !found {
			input.Status = "available"
			input.UpdatedAt = time.Now().Unix()
			s.st.GoogleAccounts = append(s.st.GoogleAccounts, &input)
			added++
		}
	}
	return
}

// ReserveGoogleAccounts atomically claims a bounded batch. Failed accounts are
// retried only when the operator explicitly enables retry; successful ones skip.
func (s *Store) ReserveGoogleAccounts(count int, retry bool, emails ...string) []GoogleAccount {
	s.mu.Lock()
	defer s.mu.Unlock()
	selected := map[string]bool{}
	for _, email := range emails {
		selected[strings.ToLower(strings.TrimSpace(email))] = true
	}
	var out []GoogleAccount
	for _, a := range s.st.GoogleAccounts {
		if a.Password == "" {
			continue
		}
		if len(selected) > 0 && !selected[a.Email] {
			continue
		}
		if len(out) >= count {
			break
		}
		if a.Status != "available" && !(retry && a.Status == "failed") {
			continue
		}
		a.Status = "queued"
		a.Phase = "queued"
		a.Message = "等待自动登录"
		a.UpdatedAt = time.Now().Unix()
		out = append(out, *a)
	}
	return out
}

// ImportGoogleAccounts previews or persists credentials atomically, preserving
// authorization history and restoring the old pool if persistence fails.
func (s *Store) ImportGoogleAccounts(accounts []GoogleAccount, preview bool) (added, updated, count int, err error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.cloneGoogleAccountsLocked()
	added, updated = s.mergeGoogleAccountsLocked(accounts)
	for _, a := range s.st.GoogleAccounts {
		if a.Password != "" {
			count++
		}
	}
	if preview {
		s.st.GoogleAccounts = before
		return
	}
	if err = s.saveLocked(); err != nil {
		s.st.GoogleAccounts = before
	}
	return
}

func (s *Store) cloneGoogleAccountsLocked() []*GoogleAccount {
	out := make([]*GoogleAccount, len(s.st.GoogleAccounts))
	for i, a := range s.st.GoogleAccounts {
		copy := *a
		out[i] = &copy
	}
	return out
}

func (s *Store) UpdateGoogleCredentials(email, password string) (bool, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.GoogleAccounts {
		if a.Password == "" || !strings.EqualFold(a.Email, strings.TrimSpace(email)) {
			continue
		}
		before := *a
		a.Password, a.UpdatedAt = password, time.Now().Unix()
		if err := s.saveLocked(); err != nil {
			*a = before
			return true, err
		}
		return true, nil
	}
	return false, nil
}

// DeleteGoogleCredentials removes only credentials; enrolled accounts stay intact.
func (s *Store) DeleteGoogleCredentials(emails []string) (map[string]bool, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.cloneGoogleAccountsLocked()
	removed := map[string]bool{}
	for _, email := range emails {
		removed[strings.ToLower(strings.TrimSpace(email))] = false
	}
	for _, a := range s.st.GoogleAccounts {
		if _, selected := removed[a.Email]; selected && a.Password != "" {
			removed[a.Email] = true
			a.Password = ""
		}
	}
	if err := s.saveLocked(); err != nil {
		s.st.GoogleAccounts = before
		return nil, err
	}
	return removed, nil
}

func (s *Store) SetGoogleAccountResult(email, status, phase, message, accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.st.GoogleAccounts {
		if a.Email != email {
			continue
		}
		a.Status = status
		a.Phase = phase
		a.Message = message
		a.UpdatedAt = time.Now().Unix()
		if accountID != "" {
			a.AccountID = accountID
		}
		return
	}
}

func (s *Store) recoverGoogleAccounts() {
	for _, a := range s.st.GoogleAccounts {
		switch a.Status {
		case "queued", "running", "action_required":
			a.Status = "failed"
			a.Phase = "interrupted"
			a.Message = "上次授权任务中断，请勾选重试失败账号"
			a.UpdatedAt = time.Now().Unix()
		}
	}
}
