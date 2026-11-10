package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Secrets are disclosed only by an explicit, authenticated same-origin request.
// Ordinary account/key list responses remain masked.
func (s *Server) handleAdminSecret(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if r.Method != http.MethodPost {
		writeErr(w, 405, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeErr(w, 400, "请选择要查看的密钥", "invalid_request_error")
		return
	}
	var secret string
	switch body.Kind {
	case "account":
		if a := s.Store.AccountCopy(body.ID); a != nil {
			secret = a.APIKey
		}
	case "key":
		for _, key := range s.Store.Keys() {
			if key.PublicID() == body.ID {
				secret = key.Key
				break
			}
		}
	default:
		writeErr(w, 400, "不支持的密钥类型", "invalid_request_error")
		return
	}
	if secret == "" {
		writeErr(w, 404, "密钥不存在或已删除", "invalid_request_error")
		return
	}
	writeJSON(w, 200, map[string]string{"key": secret})
}
