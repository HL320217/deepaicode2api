// Package egress coordinates a user-configured dynamic proxy. It never changes
// account machine identities and never infers a grant from an HTTP status alone.
package egress

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"deepaicode2api/internal/deepai"
)

type Config struct {
	Enabled          bool   `json:"enabled"`
	ProxyURL         string `json:"proxy_url"`
	RefreshURL       string `json:"refresh_url"`
	IPURL            string `json:"ip_url"`
	AutoRotate       bool   `json:"auto_rotate"`
	Threshold        int    `json:"threshold"`
	VerifyTimeoutSec int    `json:"verify_timeout_sec"`
}

func Defaults(c Config) Config {
	if c.IPURL == "" {
		c.IPURL = "https://api.ipify.org?format=json"
	}
	if c.Threshold == 0 {
		c.Threshold = 5
	}
	if c.VerifyTimeoutSec == 0 {
		c.VerifyTimeoutSec = 30
	}
	return c
}

func Validate(c Config) error {
	if c.Threshold < 1 || c.Threshold > 1000 {
		return errors.New("切换阈值须为 1–1000")
	}
	if c.VerifyTimeoutSec < 1 || c.VerifyTimeoutSec > 120 {
		return errors.New("出口确认超时须为 1–120 秒")
	}
	for _, f := range []struct {
		name, raw string
		proxy     bool
	}{{"代理地址", c.ProxyURL, true}, {"刷新链接", c.RefreshURL, false}, {"检测地址", c.IPURL, false}} {
		if f.raw == "" {
			continue
		}
		u, err := url.Parse(f.raw)
		if err != nil || u.Hostname() == "" || u.Fragment != "" {
			return fmt.Errorf("%s格式无效", f.name)
		}
		if u.Scheme != "http" && u.Scheme != "https" && !(f.proxy && (u.Scheme == "socks5" || u.Scheme == "socks5h")) {
			return fmt.Errorf("%s协议不支持", f.name)
		}
		if !f.proxy && u.User != nil {
			return fmt.Errorf("%s不支持 URL 用户信息", f.name)
		}
	}
	if c.Enabled && c.ProxyURL == "" {
		return errors.New("启用动态代理需要代理地址")
	}
	if c.Enabled && c.AutoRotate && c.RefreshURL == "" {
		return errors.New("自动切换需要刷新链接")
	}
	return nil
}

type Receipt struct {
	IP    string `json:"ip"`
	Until int64  `json:"until"`
}
type State struct {
	IP           string             `json:"ip"`
	Paused       bool               `json:"paused"`
	ManualPaused bool               `json:"manual_paused,omitempty"`
	LastError    string             `json:"last_error"`
	LastRotation int64              `json:"last_rotation"`
	Receipts     map[string]Receipt `json:"receipts,omitempty"`
	Pending      map[string]Receipt `json:"pending,omitempty"`
}
type Status struct {
	IP           string `json:"ip"`
	Count        int    `json:"count"`
	Paused       bool   `json:"paused"`
	Busy         bool   `json:"busy"`
	LastError    string `json:"last_error"`
	LastRotation int64  `json:"last_rotation"`
	Pending      int    `json:"pending"`
}

func CloneState(s State) State {
	cp := s
	cp.Receipts = map[string]Receipt{}
	for k, v := range s.Receipts {
		cp.Receipts[k] = v
	}
	cp.Pending = map[string]Receipt{}
	for k, v := range s.Pending {
		cp.Pending[k] = v
	}
	return cp
}

type Manager struct {
	op      chan struct{}
	mu      sync.Mutex
	config  Config
	state   State
	busy    bool
	persist func(Config, State) error
	gate    trafficGate
}

func New(c Config, s State, persist func(Config, State) error) *Manager {
	return &Manager{op: make(chan struct{}, 1), config: Defaults(c), state: CloneState(s), persist: persist}
}
func (m *Manager) Config() Config { m.mu.Lock(); defer m.mu.Unlock(); return m.config }
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.state.Receipts {
		if r.IP == m.state.IP && r.Until > time.Now().Unix() {
			n++
		}
	}
	pending := 0
	for _, r := range m.state.Pending {
		if r.IP == m.state.IP && r.Until > time.Now().Unix() {
			pending++
		}
	}
	return Status{IP: m.state.IP, Count: n, Paused: m.state.Paused, Busy: m.busy, LastError: m.state.LastError, LastRotation: m.state.LastRotation, Pending: pending}
}
func (m *Manager) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case m.op <- struct{}{}:
		m.mu.Lock()
		m.busy = true
		m.mu.Unlock()
		return func() { m.mu.Lock(); m.busy = false; m.mu.Unlock(); <-m.op }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (m *Manager) update(fn func(*State)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := CloneState(m.state)
	fn(&next)
	for k, r := range next.Receipts {
		if r.Until <= time.Now().Unix() {
			delete(next.Receipts, k)
		}
	}
	for k, r := range next.Pending {
		if r.Until <= time.Now().Unix() {
			delete(next.Pending, k)
		}
	}
	if m.persist != nil {
		if err := m.persist(m.config, next); err != nil {
			m.state.Paused = true
			m.state.LastError = "出口状态保存失败，请检查磁盘后恢复"
			return errors.New(m.state.LastError)
		}
	}
	m.state = next
	return nil
}
func (m *Manager) Configure(ctx context.Context, c Config) error {
	c = Defaults(c)
	if err := Validate(c); err != nil {
		return err
	}
	release, err := m.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	m.mu.Lock()
	defer m.mu.Unlock()
	next := CloneState(m.state)
	if c.ProxyURL != m.config.ProxyURL {
		next.IP = ""
	}
	if m.persist != nil {
		if err := m.persist(c, next); err != nil {
			return errors.New("代理配置保存失败")
		}
	}
	m.config = c
	m.state = next
	return nil
}

type leaseKey struct{}

// PrepareRegistration runs inside a session, before any web registration calls.
func (m *Manager) PrepareRegistration(ctx context.Context, up *deepai.Client) error {
	c := m.Config()
	if !c.Enabled {
		return nil
	}
	if ctx.Value(leaseKey{}) != m {
		return errors.New("注册任务缺少出口会话")
	}
	if m.Status().Paused {
		return errors.New("动态代理已暂停")
	}
	if m.Status().Count+m.Status().Pending < c.Threshold {
		return nil
	}
	ip, err := m.detect(ctx, c)
	if err != nil {
		return err
	}
	if err = m.update(func(s *State) { s.IP = ip }); err != nil {
		return err
	}
	if m.Status().Count+m.Status().Pending < c.Threshold {
		return nil
	}
	if !c.AutoRotate {
		return errors.New("本出口已达到配置阈值，请刷新 IP 后继续注册")
	}
	return m.rotate(ctx, up)
}

func (m *Manager) Session(ctx context.Context) (context.Context, string, func(), error) {
	if !m.Config().Enabled {
		return ctx, "", func() {}, nil
	}
	if m.Status().Paused {
		return ctx, "", nil, errors.New("动态代理已暂停，请在系统设置中恢复")
	}
	release, err := m.acquire(ctx)
	if err != nil {
		return ctx, "", nil, err
	}
	if m.Status().Paused {
		release()
		return ctx, "", nil, errors.New("动态代理已暂停，请在系统设置中恢复")
	}
	return context.WithValue(ctx, leaseKey{}, m), m.Config().ProxyURL, release, nil
}
func positive(a *deepai.Allowance) bool {
	if a == nil {
		return false
	}
	v, e := strconv.ParseFloat(a.Balance, 64)
	return e == nil && v > 0
}
func (m *Manager) Query(ctx context.Context, up *deepai.Client, a *deepai.Account) (*deepai.Allowance, error) {
	if !m.Config().Enabled {
		return up.Theos(ctx, a)
	}
	if ctx.Value(leaseKey{}) != m {
		release, err := m.acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	if m.Status().Paused {
		return nil, errors.New("动态代理已暂停，请在系统设置中恢复")
	}
	// A no-machine preflight identifies pre-existing balances without claiming a
	if up.Proxy != m.Config().ProxyURL {
		return nil, errors.New("代理配置已变化，请重新发起查询")
	}
	// fresh machine grant. Upstream errors other than machine_required are real.
	preClient := *up
	preClient.Machine = ""
	preAccount := *a
	preAccount.MachineID = ""
	before, err := preClient.Theos(ctx, &preAccount)
	if err != nil {
		var he *deepai.HTTPError
		if !errors.As(err, &he) || he.Code() != "machine_required" {
			return nil, err
		}
	}
	if positive(before) {
		key := receiptKey(a)
		if err := m.update(func(s *State) {
			if pending, ok := s.Pending[key]; ok {
				if pending.Until > time.Now().Unix() {
					pending.Until = allowanceUntil(before)
					s.Receipts[key] = pending
				}
				delete(s.Pending, key)
			}
		}); err != nil {
			return before, err
		}
		return before, nil
	}
	if strings.TrimSpace(a.MachineID) == "" {
		return nil, errors.New("账号缺少固定 Machine，请先导入或修复账号设备标识")
	}
	c := m.Config()
	ip, err := m.detect(ctx, c)
	if err != nil {
		return nil, err
	}
	if err = m.update(func(s *State) { s.IP = ip }); err != nil {
		return nil, err
	}
	if m.Status().Paused {
		return nil, errors.New("动态代理已暂停，请在系统设置中恢复")
	}
	if m.Status().Count+m.Status().Pending >= c.Threshold {
		if !c.AutoRotate {
			return before, errors.New("本出口已达到配置阈值，请手动刷新 IP 或稍后再试")
		}
		if err = m.rotate(ctx, up); err != nil {
			return nil, err
		}
		ip = m.Status().IP
	}
	key := receiptKey(a)
	if err = m.update(func(s *State) { s.Pending[key] = Receipt{ip, time.Now().Add(24 * time.Hour).Unix()} }); err != nil {
		return nil, err
	}
	al, err := up.Theos(ctx, a)
	if err != nil {
		_ = m.pause("额度请求未确认结果，已保留待核对记录；检查连接后恢复")
		return nil, err
	}
	afterIP, detectErr := m.detect(ctx, c)
	if detectErr != nil || afterIP != ip {
		_ = m.pause("查询期间出口变化或检测失败，计数不确定，请检查代理后恢复")
		return al, nil
	}
	if positive(al) {
		until := allowanceUntil(al)
		// Hash the API key; do not persist credentials in the receipt table.
		receipt := key
		if err = m.update(func(s *State) {
			if old, ok := s.Receipts[receipt]; !ok || old.Until <= time.Now().Unix() {
				s.Receipts[receipt] = Receipt{ip, until}
			}
			delete(s.Pending, key)
		}); err != nil {
			return al, err
		}
	} else if err = m.update(func(s *State) { delete(s.Pending, key) }); err != nil {
		return al, err
	}
	return al, nil
}
func receiptKey(a *deepai.Account) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(a.APIKey))) }
func allowanceUntil(a *deepai.Allowance) int64 {
	if t, e := time.Parse(time.RFC3339, a.RefreshAt); e == nil && t.After(time.Now()) {
		return t.Unix()
	}
	return time.Now().Add(24 * time.Hour).Unix()
}
func (m *Manager) pause(message string) error {
	return m.update(func(s *State) { s.Paused = true; s.LastError = message })
}
func (m *Manager) Action(ctx context.Context, action string, up *deepai.Client) error {
	if action == "pause" {
		return m.update(func(s *State) {
			s.Paused = true
			s.ManualPaused = true
			s.LastError = "已手动暂停；在途请求结束后停止新任务"
		})
	}
	release, err := m.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if !m.Config().Enabled {
		return errors.New("请先启用并保存动态代理")
	}
	switch action {
	case "detect", "resume":
		ip, err := m.detect(ctx, m.Config())
		if err != nil {
			return err
		}
		if _, _, err = up.FetchModels(ctx); err != nil {
			return errors.New("代理可检测出口，但上游连通性验证失败")
		}
		return m.update(func(s *State) {
			s.IP = ip
			if action == "resume" {
				s.Paused = false
				s.ManualPaused = false
				s.LastError = ""
			}
		})
	case "rotate":
		return m.rotate(ctx, up)
	default:
		return errors.New("未知代理操作")
	}
}
func (m *Manager) rotate(ctx context.Context, up *deepai.Client) error {
	c := m.Config()
	if c.RefreshURL == "" {
		return errors.New("未配置刷新链接")
	}
	old, err := m.detect(ctx, c)
	if err != nil {
		return m.rotationError("切换前无法确认当前出口，已暂停；请检查代理")
	}
	// Mark paused durably before the side effect, including process interruption.
	if err = m.pause("正在切换出口；中断后请检测出口再恢复"); err != nil {
		return err
	}
	unlock, err := m.gate.exclusive(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	tctx, cancel := context.WithTimeout(ctx, time.Duration(c.VerifyTimeoutSec)*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(tctx, http.MethodGet, c.RefreshURL, nil)
	if err != nil {
		return errors.New("刷新链接无效")
	}
	resp, err := client.Do(req)
	if err != nil {
		return m.rotationError("刷新接口请求失败，结果不确定；请检测出口后再操作")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return m.rotationError(fmt.Sprintf("刷新接口返回 HTTP %d，已暂停", resp.StatusCode))
	}
	up.HTTP.CloseIdleConnections()
	for {
		ip, e := m.detect(tctx, c)
		if e == nil && ip != old {
			// Use an unguarded verification client while holding the exclusive gate.
			check := deepai.NewClientWithProxy(c.ProxyURL)
			check.Origin = up.Origin
			check.Machine = up.Machine
			_, _, e = check.FetchModels(tctx)
			check.HTTP.CloseIdleConnections()
			if e == nil {
				if err := m.update(func(s *State) {
					s.IP = ip
					s.Paused = s.ManualPaused
					if !s.ManualPaused {
						s.LastError = ""
					}
					s.LastRotation = time.Now().Unix()
				}); err != nil {
					return err
				}
				if m.Status().Paused {
					return errors.New("出口已切换，任务保持手动暂停")
				}
				if m.Status().Count+m.Status().Pending >= c.Threshold {
					return m.rotationError("供应商返回的出口已达到本地记录阈值，已暂停")
				}
				return nil
			}
		}
		select {
		case <-tctx.Done():
			return m.rotationError("刷新后未确认新出口及上游连通性，已暂停；请检测后恢复")
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func (m *Manager) rotationError(msg string) error { _ = m.pause(msg); return errors.New(msg) }
func (m *Manager) detect(ctx context.Context, c Config) (string, error) {
	u, e := url.Parse(c.ProxyURL)
	if e != nil {
		return "", errors.New("代理地址无效")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	tr.DisableKeepAlives = true
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.IPURL, nil)
	if e != nil {
		return "", errors.New("出口检测地址无效")
	}
	resp, e := client.Do(req)
	if e != nil {
		return "", errors.New("通过代理检测出口失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("出口检测返回 HTTP %d", resp.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if e != nil || len(raw) > 4096 {
		return "", errors.New("出口检测响应无效")
	}
	ip := strings.TrimSpace(string(raw))
	var obj struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		ip = obj.IP
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return "", errors.New("出口检测未返回有效 IP")
	}
	return parsed.String(), nil
}

// trafficGate drains managed HTTP bodies before provider rotation. Waiting is
// cancellable; a response body holds the lease until Close, including SSE.
type trafficGate struct {
	mu         sync.Mutex
	active     int
	blocked    bool
	changed    chan struct{}
	generation uint64
}

func (g *trafficGate) signal() {
	if g.changed != nil {
		close(g.changed)
	}
	g.changed = make(chan struct{})
}
func (g *trafficGate) enter(ctx context.Context) (func(), error) {
	for {
		g.mu.Lock()
		if !g.blocked {
			g.active++
			g.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { g.mu.Lock(); g.active--; g.signal(); g.mu.Unlock() }) }, nil
		}
		if g.changed == nil {
			g.changed = make(chan struct{})
		}
		ch := g.changed
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
func (g *trafficGate) exclusive(ctx context.Context) (func(), error) {
	g.mu.Lock()
	g.blocked = true
	g.signal()
	g.mu.Unlock()
	release := func() { g.mu.Lock(); g.generation++; g.blocked = false; g.signal(); g.mu.Unlock() }
	for {
		g.mu.Lock()
		if g.active == 0 {
			g.mu.Unlock()
			return release, nil
		}
		ch := g.changed
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
}

type guardedTransport struct {
	base       http.RoundTripper
	gate       *trafficGate
	mu         sync.Mutex
	generation uint64
}
type guardedBody struct {
	io.ReadCloser
	release func()
}

func (b *guardedBody) Close() error { defer b.release(); return b.ReadCloser.Close() }
func (t *guardedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	release, e := t.gate.enter(r.Context())
	if e != nil {
		return nil, e
	}
	t.gate.mu.Lock()
	generation := t.gate.generation
	t.gate.mu.Unlock()
	t.mu.Lock()
	if generation != t.generation {
		t.CloseIdleConnections()
		t.generation = generation
	}
	t.mu.Unlock()
	resp, e := t.base.RoundTrip(r)
	if e != nil {
		release()
		return nil, e
	}
	resp.Body = &guardedBody{resp.Body, release}
	return resp, nil
}
func (t *guardedTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
func (m *Manager) Guard(client *http.Client) {
	if _, ok := client.Transport.(*guardedTransport); ok {
		return
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = &guardedTransport{base: base, gate: &m.gate}
}
