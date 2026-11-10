package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/register"
	"deepaicode2api/internal/store"
)

// registerJob 是一次批量注册任务的运行状态。
type registerJob struct {
	ID          string
	StartedAt   int64
	FinishedAt  int64
	Count       int
	Concurrency int
	Group       string
	Backend     string
	Domain      string

	mu      sync.Mutex
	running bool
	lastErr string
	records map[string]*register.Record
	order   []string
	logs    []string
	cancel  context.CancelFunc
}

type registerJobs struct {
	mu sync.Mutex
	m  map[string]*registerJob
}

func newRegisterJobs() *registerJobs { return &registerJobs{m: map[string]*registerJob{}} }

func (js *registerJobs) put(j *registerJob) {
	js.mu.Lock()
	defer js.mu.Unlock()
	for id, old := range js.m {
		if time.Since(time.Unix(old.StartedAt, 0)) > 24*time.Hour && !old.isRunning() {
			delete(js.m, id)
		}
	}
	js.m[j.ID] = j
}

func (js *registerJobs) get(id string) *registerJob {
	js.mu.Lock()
	defer js.mu.Unlock()
	return js.m[id]
}

func (js *registerJobs) list() []*registerJob {
	js.mu.Lock()
	defer js.mu.Unlock()
	out := make([]*registerJob, 0, len(js.m))
	for _, j := range js.m {
		out = append(out, j)
	}
	return out
}

func (j *registerJob) isRunning() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running
}

func (j *registerJob) addLog(message string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.logs = append(j.logs, time.Now().Format("15:04:05")+" "+message)
	if len(j.logs) > 500 {
		j.logs = append([]string(nil), j.logs[len(j.logs)-500:]...)
	}
}

func (j *registerJob) update(rec *register.Record) {
	if rec == nil {
		return
	}
	rec.Normalize()
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.records == nil {
		j.records = map[string]*register.Record{}
	}
	if _, ok := j.records[rec.Email]; !ok {
		j.order = append(j.order, rec.Email)
	}
	j.records[rec.Email] = rec
}

func (j *registerJob) finish(records []*register.Record) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
	j.FinishedAt = time.Now().Unix()
	for _, rec := range records {
		if rec == nil {
			continue
		}
		rec.Normalize()
		if j.records == nil {
			j.records = map[string]*register.Record{}
		}
		if _, ok := j.records[rec.Email]; !ok {
			j.order = append(j.order, rec.Email)
		}
		j.records[rec.Email] = rec
	}
}

func (j *registerJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	records := make([]any, 0, len(j.order))
	for _, email := range j.order {
		if rec := j.records[email]; rec != nil {
			records = append(records, sanitizeRegisterRecord(rec))
		}
	}
	return map[string]any{
		"id":          j.ID,
		"running":     j.running,
		"count":       j.Count,
		"concurrency": j.Concurrency,
		"done":        len(records),
		"group":       j.Group,
		"backend":     j.Backend,
		"domain":      j.Domain,
		"started_at":  j.StartedAt,
		"finished_at": j.FinishedAt,
		"error":       j.lastErr,
		"records":     records,
		"logs":        append([]string{}, j.logs...),
	}
}

// sanitizeRegisterRecord 下发给控制台的记录（绝不包含密码 / Session）。
func sanitizeRegisterRecord(rec *register.Record) map[string]any {
	return map[string]any{
		"email":            rec.Email,
		"status":           rec.Status,
		"stage":            rec.Stage,
		"error":            rec.Error,
		"error_code":       rec.ErrorCode,
		"last_error":       rec.LastError,
		"key":              deepai.MaskSecret(rec.APIKey),
		"account_id":       rec.AccountID,
		"user_pk":          rec.UserPK,
		"username":         rec.Username,
		"plan":             rec.Plan,
		"email_verified":   rec.EmailVerified,
		"theos_balance":    rec.TheosBalance,
		"theos_granted":    rec.TheosGranted,
		"group":            rec.Group,
		"mailbox_provider": rec.MailboxProvider,
		"proxy":            rec.ProxyLabel,
		"fingerprint":      rec.Fingerprint,
		"stages":           rec.Stages,
		"timings":          rec.Timings,
		"created_at":       rec.CreatedAt,
		"finished_at":      rec.FinishedAt,
	}
}

// handleAdminRegister 启动 / 查询批量注册任务。
func (s *Server) handleAdminRegister(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id != "" {
			job := s.Registers.get(id)
			if job == nil {
				writeErr(w, http.StatusNotFound, "任务不存在或已过期", "invalid_request_error")
				return
			}
			writeJSON(w, http.StatusOK, job.snapshot())
			return
		}
		jobs := s.Registers.list()
		out := make([]map[string]any, 0, len(jobs))
		for _, job := range jobs {
			out = append(out, job.snapshot())
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
	case http.MethodPost:
		s.startRegisterJob(w, r)
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if job := s.Registers.get(id); job != nil && job.cancel != nil {
			job.cancel()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法", "invalid_request_error")
	}
}

func (s *Server) startRegisterJob(w http.ResponseWriter, r *http.Request) {
	s.graphImportMu.Lock()
	defer s.graphImportMu.Unlock()
	var body struct {
		Count         int    `json:"count"`
		Concurrency   int    `json:"concurrency"`
		Email         string `json:"email"`
		Password      string `json:"password"`
		Domain        string `json:"domain"`
		MailBackend   string `json:"mail_backend"`
		Group         string `json:"group"`
		Verify        *bool  `json:"verify"`
		Proxy         string `json:"proxy"`
		ProxyPool     string `json:"proxy_pool"`
		NoFingerprint bool   `json:"no_fingerprint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败", "invalid_request_error")
		return
	}
	if body.Count <= 0 {
		body.Count = 1
	}
	if body.Count > 1000 {
		writeErr(w, http.StatusBadRequest, "单次最多注册 1000 个账号", "invalid_request_error")
		return
	}
	if body.Concurrency <= 0 {
		body.Concurrency = 1
	}
	if body.Concurrency > 10 {
		body.Concurrency = 10
	}
	if body.Concurrency > body.Count {
		body.Concurrency = body.Count
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email != "" && body.Count > 1 {
		writeErr(w, http.StatusBadRequest, "指定邮箱时 count 必须为 1", "invalid_request_error")
		return
	}
	group := strings.TrimSpace(body.Group)
	if group == "" {
		group = "default"
	}

	cfg := register.LoadConfig()
	if _, err := os.Stat(s.graphMailPath()); err == nil {
		cfg.GraphMailFile = s.graphMailPath()
	}
	if backend := strings.ToLower(strings.TrimSpace(body.MailBackend)); backend != "" && backend != "graph" {
		writeErr(w, http.StatusBadRequest, "仅支持 Hotmail / Microsoft Graph 邮箱", "invalid_request_error")
		return
	}
	if body.Proxy != "" {
		cfg.Proxy = strings.TrimSpace(body.Proxy)
	}
	poolRaw := strings.TrimSpace(body.ProxyPool)
	if poolRaw == "" {
		poolRaw = cfg.ProxyPool
	}
	if poolRaw == "" && cfg.ProxyFile != "" {
		if raw, err := register.ReadProxyFile(cfg.ProxyFile); err == nil {
			poolRaw = raw
		}
	}
	pool, err := register.NewProxyPool(poolRaw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "代理池配置错误: "+err.Error(), "invalid_request_error")
		return
	}
	backend := cfg.AutoBackend()
	if missing := cfg.Validate(backend); len(missing) > 0 {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("邮箱后端 %s 缺少配置：%s（请在 .env / 环境变量中填写）", backend, strings.Join(missing, ", ")),
			"invalid_request_error")
		return
	}
	verify := true
	if body.Verify != nil {
		verify = *body.Verify
	}

	provider, err := register.NewProvider(cfg, backend, "", body.Email)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "初始化邮箱后端失败: "+err.Error(), "invalid_request_error")
		return
	}

	if backend == "graph" {
		if err := s.mergeGraphHistory(provider); err != nil {
			writeErr(w, http.StatusInternalServerError, "读取注册历史失败", "server_error")
			return
		}
	}
	jobID := deepai.RandomHex(8)
	ctx, cancel := context.WithCancel(context.Background())
	job := &registerJob{
		ID:          jobID,
		StartedAt:   time.Now().Unix(),
		Count:       body.Count,
		Concurrency: body.Concurrency,
		Group:       group,
		Backend:     backend,
		running:     true,
		records:     map[string]*register.Record{},
		cancel:      cancel,
	}
	s.Registers.put(job)

	go s.runRegisterJob(ctx, job, cfg, provider, body.Email, body.Password, verify, pool, body.NoFingerprint)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": jobID})
}

func (s *Server) runRegisterJob(ctx context.Context, job *registerJob, cfg *register.Config, provider register.Provider, email, password string, verify bool, pool *register.ProxyPool, noFingerprint bool) {
	if capped, ok := pool.CapConcurrency(job.Concurrency); ok {
		log.Printf("[register] 并发 %d 超过出口数 %d，压到 %d", job.Concurrency, capped, capped)
		job.Concurrency = capped
	}

	baseDir := filepath.Dir(s.Store.Path())
	ledger := register.NewLedger(filepath.Join(baseDir, "exports", "register-ledger.jsonl"))
	// 缓冲写：任务结束必须刷盘并关闭句柄。
	defer func() { _ = ledger.Close() }()

	pipe := register.New(register.Options{
		NetworkSession:     s.Egress.Session,
		BeforeRegistration: func(ctx context.Context) error { return s.Egress.PrepareRegistration(ctx, s.managedClient(s.Up)) },
		QueryTheos:         s.registrationQuota,
		WrapHTTP: func(c *http.Client) {
			if s.Egress.Config().Enabled {
				s.Egress.Guard(c)
			}
		},
		Config:        cfg,
		Provider:      provider,
		Ledger:        ledger,
		Enroller:      localEnroller{s: s},
		Group:         job.Group,
		Verify:        verify,
		ProxyPool:     pool,
		NoFingerprint: noFingerprint,
		VerifyTimeout: cfg.VerifyWait,
		PollInterval:  cfg.PollEvery,
		MinInterval:   cfg.MinInterval,
		Log: func(format string, args ...any) {
			message := fmt.Sprintf(format, args...)
			job.addLog(message)
			log.Printf("[register] %s", message)
		},
		OnUpdate: job.update,
	})

	job.addLog(fmt.Sprintf("任务启动：数量=%d 并发=%d 后端=%s", job.Count, job.Concurrency, job.Backend))
	log.Printf("[register] 任务 %s 启动：count=%d concurrency=%d group=%s backend=%s", job.ID, job.Count, job.Concurrency, job.Group, job.Backend)
	records := pipe.RunBatch(ctx, job.Count, job.Concurrency, email, password)
	job.finish(records)
	job.addLog(fmt.Sprintf("任务完成：%d 个记录", len(records)))
	log.Printf("[register] 任务 %s 完成：%d 个记录", job.ID, len(records))

	if _, err := register.WriteDeliverables(filepath.Join(baseDir, "result"), records); err != nil {
		log.Printf("[register] 写交付物失败: %v", err)
	}
}

// localEnroller 是进程内入池器：既写入号池，也能回填验证状态。
//
// 实现 register.VerifiedMarker，因此后台验证完成后会自动回填 email_verified。
type localEnroller struct{ s *Server }

func (e localEnroller) Enroll(_ context.Context, rec *register.Record) (string, error) {
	return e.s.enrollRecord(rec)
}

func (e localEnroller) MarkVerified(_ context.Context, accountID, email string) error {
	return e.s.markAccountVerified(accountID, email)
}

// markAccountVerified 把账号标记为邮箱已验证（按 ID 或邮箱定位）。
func (s *Server) markAccountVerified(id, email string) error {
	found, err := s.Store.MarkAccountVerifiedPersisted(id, email)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrResourceNotFound
	}
	return nil
}

// enrollRecord 把注册链路的结果写入号池（服务端内进程调用）。
func (s *Server) enrollRecord(rec *register.Record) (string, error) {
	if rec == nil {
		return "", errors.New("空记录")
	}
	reg := deepai.Registration{
		Email:          rec.Email,
		Password:       rec.Password,
		Username:       rec.Username,
		APIKey:         rec.APIKey,
		SessionID:      rec.SessionID,
		CSRFToken:      rec.CSRFToken,
		UserPK:         rec.UserPK,
		Plan:           rec.Plan,
		EmailVerified:  rec.EmailVerified,
		Group:          rec.Group,
		Proxy:          rec.Proxy,
		Fingerprint:    rec.Fingerprint,
		MachineID:      rec.MachineID,
		UserAgent:      rec.UserAgent,
		AcceptLanguage: rec.AcceptLanguage,
		SecCHUA:        rec.SecCHUA,
		Platform:       rec.Platform,
		Source:         "register",
	}
	id, _, err := s.enrollRegistration(reg, &deepai.Allowance{
		Enabled:   true,
		Balance:   rec.TheosBalance,
		Granted:   rec.TheosGranted,
		RefreshAt: rec.TheosRefreshAt,
		CheckedAt: time.Now().Unix(),
	})
	return id, err
}

// enrollRegistration 是入池的唯一写入口（管理接口与注册任务共用）。
func (s *Server) enrollRegistration(reg deepai.Registration, al *deepai.Allowance) (string, bool, error) {
	acct, err := deepai.AccountFromRegistration(reg)
	if err != nil {
		return "", false, err
	}
	now := time.Now().Unix()
	acct.Status = "ok"
	acct.LastUsed = now
	acct.LastVerified = now
	acct.Failures = 0
	acct.LastErr = ""
	if al != nil {
		acct.Theos = al
	}

	id, isNew, err := s.Store.EnrollAccount(acct)
	if err != nil {
		return "", isNew, err
	}

	// 入池时的 0 额度可能稍后发生变化，安排有限次复探以更新缓存。
	// 是否发放额度由上游决定；复探不修改设备标识或推算余额。
	if al == nil || !positiveQuota(al) {
		s.scheduleQuotaReprobe(id)
	}
	return id, isNew, nil
}

// positiveQuota 报告配额是否明确大于 0。
//
// 解析不了 / 字段缺失都按“未知”（返回 false）处理：宁可多探一次，
// 也不要把可用账号判死。
func positiveQuota(al *deepai.Allowance) bool {
	if al == nil || !al.Enabled {
		return false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(al.Balance), 64)
	return err == nil && v > 0
}

// quotaReprobeDelays 是入池后 0 额度的复探退避序列。
//
// 各次探测之间依次等待 3、6、12 分钟（约在入池后第 3、9、21 分钟）。
// 最多 3 次；拿到正额度即停止，不保证上游一定会发放额度。
var quotaReprobeDelays = []time.Duration{3 * time.Minute, 6 * time.Minute, 12 * time.Minute}

// scheduleQuotaReprobe 异步复探某个账号的配额（仅当它仍是 0 时）。
//
// 每个账号一个 goroutine，次数有上限，不会自我放大。
func (s *Server) scheduleQuotaReprobe(id string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	delays := s.quotaReprobeDelays
	if len(delays) == 0 {
		return
	}
	go func() {
		for _, d := range delays {
			time.Sleep(d)
			a := s.Store.AccountCopy(id)
			if a == nil {
				return // 已被删除
			}
			if positiveQuota(a.Theos) {
				return // 期间被别的路径探到了
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			al, err := s.queryQuota(ctx, a)
			cancel()
			s.Store.SetAccountAllowance(id, al, err)
			if err := s.Store.Save(); err != nil {
				log.Printf("[quota] 保存复探结果失败: %v", err)
			}
			if err == nil && positiveQuota(al) {
				log.Printf("[quota] %s 复探拿到额度: balance=%s granted=%s", a.Name, al.Balance, al.Granted)
				return
			}
		}
	}()
}

// graphMailPath is stored beside the persistent server state, never in web assets.
func (s *Server) graphMailPath() string {
	return filepath.Join(filepath.Dir(s.Store.Path()), "graph-mail-accounts.txt")
}

func (s *Server) handleGraphMail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.graphImportMu.Lock()
	defer s.graphImportMu.Unlock()
	path := s.graphMailPath()
	switch r.Method {
	case http.MethodGet:
		cfg := &register.Config{GraphMailFile: path}
		provider, err := register.NewProvider(cfg, "graph", "", "")
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{"count": 0})
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "邮箱文件读取失败", "server_error")
			return
		}
		if err := s.mergeGraphHistory(provider); err != nil {
			writeErr(w, http.StatusInternalServerError, "读取注册历史失败", "server_error")
			return
		}
		statuses, err := provider.(interface {
			Statuses() ([]register.GraphMailStatus, error)
		}).Statuses()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取邮箱状态失败", "server_error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(statuses), "accounts": statuses})
	case http.MethodPost, http.MethodDelete:
		if s.graphPoolActive() {
			writeErr(w, 409, "请先停止注册任务再维护邮箱", "invalid_request_error")
			return
		}
		var body struct {
			Content      string   `json:"content"`
			Preview      bool     `json:"preview"`
			Action       string   `json:"action"`
			Email        string   `json:"email"`
			Password     string   `json:"password"`
			ClientID     string   `json:"client_id"`
			RefreshToken string   `json:"refresh_token"`
			Emails       []string `json:"emails"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "文件无效或超过 4 MB", "invalid_request_error")
			return
		}
		if r.Method == http.MethodDelete || body.Action == "update" {
			existing, err := os.ReadFile(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				writeErr(w, 500, "读取邮箱文件失败", "server_error")
				return
			}
			lines := graphCredentialLines(string(existing))
			if r.Method == http.MethodDelete {
				emails, err := normalizeResourceEmails(body.Emails)
				if err != nil {
					writeErr(w, 400, err.Error(), "invalid_request_error")
					return
				}
				removed := map[string]bool{}
				for _, email := range emails {
					removed[email] = false
				}
				remaining := make([]string, 0, len(lines))
				for _, line := range lines {
					email := graphCredentialEmail(line)
					if _, ok := removed[email]; ok {
						removed[email] = true
					} else {
						remaining = append(remaining, line)
					}
				}
				if err := writeGraphCredentials(path, remaining); err != nil {
					writeErr(w, 500, "保存失败，邮箱未删除", "server_error")
					return
				}
				writeJSON(w, 200, mailboxDeleteResult(emails, removed))
				return
			}
			body.Email = strings.ToLower(strings.TrimSpace(body.Email))
			for _, value := range []string{body.Email, body.Password, body.ClientID, body.RefreshToken} {
				if strings.ContainsAny(value, "\r\n\x00") || strings.Contains(value, "----") {
					writeErr(w, 400, "邮箱凭据格式无效", "invalid_request_error")
					return
				}
			}
			if !strings.Contains(body.Email, "@") || strings.TrimSpace(body.ClientID) == "" || strings.TrimSpace(body.RefreshToken) == "" {
				writeErr(w, 400, "请填写邮箱、客户端 ID 和刷新令牌", "invalid_request_error")
				return
			}
			found := false
			for _, line := range lines {
				if graphCredentialEmail(line) == body.Email {
					found = true
					break
				}
			}
			if !found {
				writeErr(w, 404, "邮箱不存在", "invalid_request_error")
				return
			}
			body.Content = strings.Join([]string{body.Email, body.Password, body.ClientID, body.RefreshToken}, "----")
		} else if body.Action != "" {
			writeErr(w, 400, "不支持的操作", "invalid_request_error")
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			writeErr(w, http.StatusInternalServerError, "无法保存邮箱文件", "server_error")
			return
		}
		f, err := os.CreateTemp(filepath.Dir(path), ".graph-mail-*")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "无法保存邮箱文件", "server_error")
			return
		}
		defer os.Remove(f.Name())
		_, writeError := f.WriteString(body.Content)
		closeError := f.Close()
		if writeError != nil || closeError != nil {
			writeErr(w, http.StatusInternalServerError, "无法保存邮箱文件", "server_error")
			return
		}
		provider, err := register.NewProvider(&register.Config{GraphMailFile: f.Name()}, "graph", "", "")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		statuses, err := provider.(interface {
			Statuses() ([]register.GraphMailStatus, error)
		}).Statuses()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取邮箱状态失败", "server_error")
			return
		}
		existing, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			writeErr(w, http.StatusInternalServerError, "读取已有邮箱失败", "server_error")
			return
		}
		if len(existing) > 0 {
			if _, err := register.NewProvider(&register.Config{GraphMailFile: path}, "graph", "", ""); err != nil {
				writeErr(w, http.StatusInternalServerError, "已有邮箱文件格式错误", "server_error")
				return
			}
		}
		lines := make([]string, 0, len(statuses))
		positions := map[string]int{}
		appendLines := func(content string) {
			for _, line := range strings.Split(content, "\n") {
				line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "\ufeff"))
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				email, _, _ := strings.Cut(line, "----")
				key := strings.ToLower(strings.TrimSpace(email))
				if i, ok := positions[key]; ok {
					lines[i] = line
				} else {
					positions[key] = len(lines)
					lines = append(lines, line)
				}
			}
		}
		appendLines(string(existing))
		before := len(lines)
		appendLines(body.Content)
		count := len(lines)
		if body.Preview {
			writeJSON(w, 200, map[string]any{"count": count, "added": count - before, "updated": len(statuses) - (count - before)})
			return
		}
		if err := os.WriteFile(f.Name(), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			writeErr(w, http.StatusInternalServerError, "无法保存邮箱文件", "server_error")
			return
		}
		if err := os.Rename(f.Name(), path); err != nil {
			writeErr(w, http.StatusInternalServerError, "无法保存邮箱文件", "server_error")
			return
		}
		if body.Action == "update" {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": count, "added": count - before, "updated": len(statuses) - (count - before)})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法", "invalid_request_error")
	}
}

func (s *Server) mergeGraphHistory(provider register.Provider) error {
	merger, ok := provider.(interface{ MergeHistory(map[string]string) error })
	if !ok {
		return nil
	}
	history := map[string]string{}
	for _, a := range s.Store.Accounts() {
		if a != nil && a.Email != "" {
			history[strings.ToLower(a.Email)] = "registered"
		}
	}
	ledger := register.NewLedger(filepath.Join(filepath.Dir(s.Store.Path()), "exports", "register-ledger.jsonl"))
	records, err := ledger.Load()
	if err != nil {
		return err
	}
	for _, r := range records {
		if r == nil || r.Email == "" {
			continue
		}
		key := strings.ToLower(r.Email)
		if register.StatusRank(r.Status) >= register.StatusRank(register.StatusRegistered) || r.Stages["exists"] == "true" || r.Stages["register"] == "ok" || r.Stages["register"] == "exists" || r.Stages["register"] == "recovered-by-login" {
			history[key] = "registered"
		} else if r.Stages["register"] != "" && history[key] != "registered" {
			history[key] = "pending"
		}
	}
	return merger.MergeHistory(history)
}

// RestoreLedgerIdentities restores missing identities from matching registration records.
func (s *Server) RestoreLedgerIdentities() (int, error) {
	ledger := register.NewLedger(filepath.Join(filepath.Dir(s.Store.Path()), "exports", "register-ledger.jsonl"))
	records, err := ledger.Load()
	if err != nil {
		return 0, err
	}
	identities := make([]*deepai.Account, 0, len(records))
	for _, r := range records {
		if r != nil {
			identities = append(identities, &deepai.Account{Email: r.Email, APIKey: r.APIKey, MachineID: r.MachineID, UserAgent: r.UserAgent, AcceptLanguage: r.AcceptLanguage, SecCHUA: r.SecCHUA, Platform: r.Platform})
		}
	}
	n := s.Store.RestoreRegistrationIdentity(identities)
	if n > 0 {
		if err := s.Store.Save(); err != nil {
			return n, err
		}
	}
	return n, nil
}
