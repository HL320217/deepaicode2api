package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"deepaicode2api/internal/deepai"
)

var ErrResourceNotFound = errors.New("资源不存在")
var ErrInvalidResource = errors.New("资源参数无效")

// PublicID is a stable, non-secret reference, including for legacy saved keys.
func (k *APIKey) PublicID() string { return fmt.Sprintf("key-%x", sha256.Sum256([]byte(k.Key))) }

type ResourceUpdate struct {
	Name    *string `json:"name"`
	Group   *string `json:"group"`
	Enabled *bool   `json:"enabled"`
}

type ResourceResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// resourceTransaction serializes publication and persistence. Only copied resource
// fields may be changed by fn; unrelated live state remains shared and locked.
func (s *Store) resourceTransaction(fn func(*State) error) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.st
	next := *previous
	next.Accounts = make([]*deepai.Account, len(previous.Accounts))
	for i, a := range previous.Accounts {
		cp := *a
		if a.Theos != nil {
			al := *a.Theos
			cp.Theos = &al
		}
		next.Accounts[i] = &cp
	}
	next.Keys = make([]*APIKey, len(previous.Keys))
	for i, k := range previous.Keys {
		cp := *k
		next.Keys[i] = &cp
	}
	next.Settings.ModelAliases = make(map[string]string, len(previous.Settings.ModelAliases))
	for k, v := range previous.Settings.ModelAliases {
		next.Settings.ModelAliases[k] = v
	}
	if err := fn(&next); err != nil {
		return err
	}
	s.st = &next
	if err := s.saveLocked(); err != nil {
		s.st = previous
		return err
	}
	return nil
}

func normalizedGroup(group string) string {
	group = strings.TrimSpace(group)
	if group == "" {
		return "default"
	}
	return group
}

func (s *Store) UpdateAccountResource(id string, update ResourceUpdate) error {
	return s.resourceTransaction(func(st *State) error {
		for _, a := range st.Accounts {
			if a.ID == id {
				if update.Name != nil {
					a.Name = strings.TrimSpace(*update.Name)
				}
				if update.Group != nil {
					a.Group = normalizedGroup(*update.Group)
				}
				if update.Enabled != nil {
					a.Enabled = *update.Enabled
				}
				return nil
			}
		}
		return ErrResourceNotFound
	})
}

// EnrollAccount merges registration identity and the newly verified runtime
// state in the same transaction. No live account pointer escapes the write lock.
func (s *Store) EnrollAccount(candidate *deepai.Account) (string, bool, error) {
	if candidate == nil {
		return "", false, ErrInvalidResource
	}
	account := *candidate
	if candidate.Theos != nil {
		allowance := *candidate.Theos
		account.Theos = &allowance
	}
	verified := account
	isNew := false
	err := s.resourceTransaction(func(st *State) error {
		isNew = upsertAccountLocked(st, &account)
		account.Status = verified.Status
		account.Failures = verified.Failures
		account.LastErr = verified.LastErr
		account.LastUsed = verified.LastUsed
		account.LastVerified = verified.LastVerified
		if verified.Theos != nil {
			account.Theos = verified.Theos
		}
		return nil
	})
	if err != nil {
		return "", isNew, err
	}
	return account.ID, isNew, nil
}

func (s *Store) BatchAccounts(ids []string, operation, group string) ([]ResourceResult, error) {
	if len(ids) == 0 {
		return nil, ErrInvalidResource
	}
	switch operation {
	case "enable", "disable", "remove", "clear-errors", "toggle":
	case "set-group":
		if strings.TrimSpace(group) == "" {
			return nil, ErrInvalidResource
		}
	default:
		return nil, ErrInvalidResource
	}
	results := make([]ResourceResult, 0, len(ids))
	err := s.resourceTransaction(func(st *State) error {
		seen := map[string]bool{}
		for _, id := range ids {
			result := ResourceResult{ID: id}
			if seen[id] {
				result.Error = "重复的 ID"
				results = append(results, result)
				continue
			}
			seen[id] = true
			for i, a := range st.Accounts {
				if a.ID != id {
					continue
				}
				result.OK = true
				switch operation {
				case "enable":
					a.Enabled = true
				case "disable":
					a.Enabled = false
				case "toggle":
					a.Enabled = !a.Enabled
				case "set-group":
					a.Group = strings.TrimSpace(group)
				case "clear-errors":
					a.Failures = 0
					a.Status = "unknown"
					a.LastErr = ""
				case "remove":
					st.Accounts = append(st.Accounts[:i], st.Accounts[i+1:]...)
				}
				break
			}
			if !result.OK {
				result.Error = ErrResourceNotFound.Error()
			}
			results = append(results, result)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) CreateKeyResource(name, group string) (*APIKey, error) {
	k := &APIKey{Key: "sk-" + deepai.RandomHex(24), Name: strings.TrimSpace(name), Group: normalizedGroup(group), Enabled: true, CreatedAt: time.Now().Unix()}
	err := s.resourceTransaction(func(st *State) error { cp := *k; st.Keys = append(st.Keys, &cp); return nil })
	if err != nil {
		return nil, err
	}
	cp := *k
	return &cp, nil
}

func (s *Store) UpdateKeyResource(id string, update ResourceUpdate) error {
	return s.resourceTransaction(func(st *State) error {
		for _, k := range st.Keys {
			if k.PublicID() == id {
				if update.Name != nil {
					k.Name = strings.TrimSpace(*update.Name)
				}
				if update.Group != nil {
					k.Group = normalizedGroup(*update.Group)
				}
				if update.Enabled != nil {
					k.Enabled = *update.Enabled
				}
				return nil
			}
		}
		return ErrResourceNotFound
	})
}

// DeleteKeyResource accepts public IDs and full keys for legacy HTTP clients.
func (s *Store) DeleteKeyResource(ref string, toggle bool) error {
	return s.resourceTransaction(func(st *State) error {
		for i, k := range st.Keys {
			if k.PublicID() == ref || k.Key == ref {
				if toggle {
					k.Enabled = !k.Enabled
				} else {
					st.Keys = append(st.Keys[:i], st.Keys[i+1:]...)
				}
				return nil
			}
		}
		return ErrResourceNotFound
	})
}

func (s *Store) SaveSettings(fn func(*Settings)) error {
	return s.resourceTransaction(func(st *State) error { fn(&st.Settings); return nil })
}

func (s *Store) ClearResourceHistory(stats bool) error {
	return s.resourceTransaction(func(st *State) error {
		if stats {
			st.Stats = Stats{StartedAt: time.Now().Unix()}
		} else {
			st.Logs = nil
		}
		return nil
	})
}

// MarkAccountVerifiedPersisted resolves an ID first, then a case-insensitive email.
func (s *Store) MarkAccountVerifiedPersisted(id, email string) (bool, error) {
	found := false
	err := s.resourceTransaction(func(st *State) error {
		var target *deepai.Account
		if id = strings.TrimSpace(id); id != "" {
			for _, a := range st.Accounts {
				if a.ID == id {
					target = a
					break
				}
			}
		}
		if target == nil && strings.TrimSpace(email) != "" {
			for _, a := range st.Accounts {
				if sameEmail(a.Email, email) {
					target = a
					break
				}
			}
		}
		if target == nil {
			return ErrResourceNotFound
		}
		target.EmailVerified = true
		found = true
		return nil
	})
	if errors.Is(err, ErrResourceNotFound) {
		return false, nil
	}
	return found, err
}

func (s *Store) AssignAccountMachines(id string) ([]*deepai.Account, error) {
	targets := make([]*deepai.Account, 0)
	err := s.resourceTransaction(func(st *State) error {
		found := id == ""
		for _, a := range st.Accounts {
			if id != "" && a.ID != id {
				continue
			}
			found = true
			if strings.TrimSpace(a.MachineID) != "" {
				continue
			}
			a.MachineID = deepai.NewMachineUUID()
			cp := *a
			targets = append(targets, &cp)
		}
		if !found {
			return ErrResourceNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return targets, nil
}

func (s *Store) MoveAccountGroup(id, from, group string) (int, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return 0, ErrInvalidResource
	}
	moved := 0
	err := s.resourceTransaction(func(st *State) error {
		found := id == ""
		for _, a := range st.Accounts {
			if id != "" && a.ID != id {
				continue
			}
			found = true
			if from != "" && !strings.EqualFold(a.GroupName(), from) {
				continue
			}
			if a.GroupName() == group {
				continue
			}
			a.Group = group
			moved++
		}
		if !found {
			return ErrResourceNotFound
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return moved, nil
}
