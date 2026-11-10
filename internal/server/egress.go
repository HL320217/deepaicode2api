package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"deepaicode2api/internal/deepai"
)

// Secret-bearing URLs are write-only. Blank fields preserve stored values.
func (s *Server) egressView() map[string]any {
	c := s.Egress.Config()
	label := ""
	if u, e := url.Parse(c.ProxyURL); e == nil && u.Host != "" {
		label = u.Scheme + "://" + u.Host
	}
	return map[string]any{"config": map[string]any{"enabled": c.Enabled, "auto_rotate": c.AutoRotate, "threshold": c.Threshold, "verify_timeout_sec": c.VerifyTimeoutSec, "ip_url": c.IPURL, "proxy_label": label, "proxy_set": c.ProxyURL != "", "refresh_set": c.RefreshURL != ""}, "status": s.Egress.Status()}
}
func (s *Server) handleEgress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		writeJSON(w, 200, s.egressView())
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "仅支持 GET / POST", "invalid_request_error")
		return
	}
	var body struct {
		Enabled          bool   `json:"enabled"`
		AutoRotate       bool   `json:"auto_rotate"`
		Threshold        int    `json:"threshold"`
		VerifyTimeoutSec int    `json:"verify_timeout_sec"`
		ProxyURL         string `json:"proxy_url"`
		RefreshURL       string `json:"refresh_url"`
		IPURL            string `json:"ip_url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&body); err != nil {
		writeErr(w, 400, "代理配置不是有效 JSON", "invalid_request_error")
		return
	}
	c := s.Egress.Config()
	c.Enabled = body.Enabled
	c.AutoRotate = body.AutoRotate
	c.Threshold = body.Threshold
	c.VerifyTimeoutSec = body.VerifyTimeoutSec
	if strings.TrimSpace(body.ProxyURL) != "" {
		c.ProxyURL = strings.TrimSpace(body.ProxyURL)
	}
	if strings.TrimSpace(body.RefreshURL) != "" {
		c.RefreshURL = strings.TrimSpace(body.RefreshURL)
	}
	c.IPURL = strings.TrimSpace(body.IPURL)
	if u, e := url.Parse(c.IPURL); e == nil {
		for key := range u.Query() {
			if key != "format" {
				writeErr(w, 400, "出口检测地址仅支持公开接口，不要包含凭据参数", "invalid_request_error")
				return
			}
		}
	}
	if err := s.Egress.Configure(r.Context(), c); err != nil {
		writeErr(w, 400, err.Error(), "invalid_request_error")
		return
	}
	writeJSON(w, 200, s.egressView())
}
func (s *Server) handleEgressAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeErr(w, 405, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, 400, "操作参数无效", "invalid_request_error")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.Egress.Config().VerifyTimeoutSec+30)*time.Second)
	defer cancel()
	if err := s.Egress.Action(ctx, body.Action, s.managedClient(s.Up)); err != nil {
		writeJSON(w, 409, map[string]any{"error": map[string]string{"message": err.Error()}, "status": s.Egress.Status()})
		return
	}
	writeJSON(w, 200, s.egressView())
}
func (s *Server) managedClient(base *deepai.Client) *deepai.Client {
	cfg := s.Egress.Config()
	if !cfg.Enabled {
		return base
	}
	cacheKey := "managed:" + cfg.ProxyURL + "|" + base.Origin
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	if c := s.proxyClients[cacheKey]; c != nil {
		return c
	}
	c := deepai.NewClientWithProxy(cfg.ProxyURL)
	c.Origin = base.Origin
	c.Machine = base.Machine
	s.Egress.Guard(c.HTTP)
	s.proxyClients[cacheKey] = c
	return c
}
func (s *Server) queryQuota(ctx context.Context, a *deepai.Account) (*deepai.Allowance, error) {
	if !s.Egress.Config().Enabled {
		return s.clientFor(a).Theos(ctx, a)
	}
	return s.Egress.Query(ctx, s.managedClient(s.Up), a)
}
func (s *Server) registrationQuota(ctx context.Context, up *deepai.Client, a *deepai.Account) (*deepai.Allowance, error) {
	if !s.Egress.Config().Enabled {
		return up.Theos(ctx, a)
	}
	return s.Egress.Query(ctx, s.managedClient(up), a)
}
