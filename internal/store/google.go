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
		out = append(out, GoogleAccountView{a.Email, a.Status, a.Phase, a.Message, a.AccountID, a.UpdatedAt})
	}
	return out
}

// MergeGoogleAccounts updates credentials without resetting completed results.
func (s *Store) MergeGoogleAccounts(accounts []GoogleAccount) (added, updated int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, input := range accounts {
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		found := false
		for _, old := range s.st.GoogleAccounts {
			if old.Email == input.Email {
				old.Password = input.Password
				old.UpdatedAt = time.Now().Unix()
				updated++
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
func (s *Store) ReserveGoogleAccounts(count int, retry bool) []GoogleAccount {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GoogleAccount
	for _, a := range s.st.GoogleAccounts {
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
