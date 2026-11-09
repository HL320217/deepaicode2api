package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/googleauth"
	"deepaicode2api/internal/store"
)

type googleTaskView struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Total     int      `json:"total"`
	Current   int      `json:"current"`
	Email     string   `json:"email,omitempty"`
	Phase     string   `json:"phase,omitempty"`
	Message   string   `json:"message,omitempty"`
	Succeeded int      `json:"succeeded"`
	Failed    int      `json:"failed"`
	Skipped   int      `json:"skipped"`
	Logs      []string `json:"logs"`
}

type googleTask struct {
	mu            sync.Mutex
	view          googleTaskView
	cancel        context.CancelFunc
	cancelAccount context.CancelFunc
}

type googleTasks struct {
	mu      sync.Mutex
	current *googleTask
}

func (g *googleTasks) snapshot() googleTaskView {
	g.mu.Lock()
	task := g.current
	g.mu.Unlock()
	if task == nil {
		return googleTaskView{Status: "idle", Logs: []string{}}
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	view := task.view
	view.Logs = append([]string{}, view.Logs...)
	return view
}

func (g *googleTasks) activeLocked() bool {
	if g.current == nil {
		return false
	}
	g.current.mu.Lock()
	defer g.current.mu.Unlock()
	return g.current.view.Status == "running" || g.current.view.Status == "cancelling"
}

func (s *Server) handleGoogleAccounts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]any{"accounts": s.Store.GoogleAccounts(), "encrypted": s.Store.MasterKeySet()})
	case http.MethodPost:
		s.Google.mu.Lock()
		defer s.Google.mu.Unlock()
		if s.Google.activeLocked() {
			writeErr(w, 409, "请先停止 Google 授权任务再导入", "invalid_request_error")
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
			writeErr(w, 400, "文件无效或超过 4 MB", "invalid_request_error")
			return
		}
		credentials, err := googleauth.ParseAccounts(body.Content)
		if err != nil {
			writeErr(w, 400, err.Error(), "invalid_request_error")
			return
		}
		accounts := make([]store.GoogleAccount, len(credentials))
		for i, c := range credentials {
			accounts[i] = store.GoogleAccount{Email: c.Email, Password: c.Password}
		}
		added, updated := s.Store.MergeGoogleAccounts(accounts)
		if err := s.Store.Save(); err != nil {
			writeErr(w, 500, "账号已载入内存，但状态文件保存失败，请检查磁盘后重新导入", "server_error")
			return
		}
		writeJSON(w, 200, map[string]any{"added": added, "updated": updated, "count": len(s.Store.GoogleAccounts())})
	default:
		writeErr(w, 405, "仅支持 GET / POST", "invalid_request_error")
	}
}

func (s *Server) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "仅支持 POST", "invalid_request_error")
		return
	}
	var body struct {
		Count       int  `json:"count"`
		RetryFailed bool `json:"retry_failed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Count < 1 || body.Count > 1000 {
		writeErr(w, 400, "授权数量必须为 1–1000", "invalid_request_error")
		return
	}
	s.Google.mu.Lock()
	defer s.Google.mu.Unlock()
	if s.Google.activeLocked() {
		writeErr(w, 409, "已有 Google 授权任务正在执行", "invalid_request_error")
		return
	}
	if s.googleLogin == nil {
		if _, err := googleauth.NewBrowserFromEnv(); err != nil {
			writeErr(w, 400, err.Error(), "invalid_request_error")
			return
		}
		if _, err := googleauth.FindBrowser(); err != nil {
			writeErr(w, 400, err.Error(), "invalid_request_error")
			return
		}
	}
	accounts := s.Store.ReserveGoogleAccounts(body.Count, body.RetryFailed)
	if len(accounts) == 0 {
		writeErr(w, 400, "没有可用 Google 账号；失败账号需勾选重试", "invalid_request_error")
		return
	}
	if err := s.Store.Save(); err != nil {
		for _, a := range accounts {
			s.Store.SetGoogleAccountResult(a.Email, "available", "", "", "")
		}
		writeErr(w, 500, "无法保存任务状态，未启动浏览器", "server_error")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	task := &googleTask{view: googleTaskView{ID: deepai.RandomHex(16), Status: "running", Total: len(accounts), Logs: []string{}}, cancel: cancel}
	s.Google.current = task
	go s.runGoogleTask(ctx, task, accounts)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"id": task.view.ID, "total": len(accounts)})
}

func (s *Server) handleGoogleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "仅支持 GET", "invalid_request_error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.Google.snapshot())
}

func (s *Server) handleGoogleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "仅支持 POST", "invalid_request_error")
		return
	}
	s.Google.mu.Lock()
	task := s.Google.current
	s.Google.mu.Unlock()
	if task == nil {
		writeErr(w, 409, "没有执行中的 Google 任务", "invalid_request_error")
		return
	}
	task.mu.Lock()
	if task.view.Status != "running" {
		task.mu.Unlock()
		writeErr(w, 409, "任务已结束或正在停止", "invalid_request_error")
		return
	}
	cancel := task.cancelAccount
	if strings.HasSuffix(r.URL.Path, "/cancel") {
		task.view.Status = "cancelling"
		cancel = task.cancel
	}
	task.mu.Unlock()
	if cancel == nil {
		writeErr(w, 409, "当前没有可以跳过的账号", "invalid_request_error")
		return
	}
	cancel()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) runGoogleTask(ctx context.Context, task *googleTask, accounts []store.GoogleAccount) {
	defer task.cancel()
	for i := range accounts {
		account := accounts[i]
		if ctx.Err() != nil {
			s.Store.SetGoogleAccountResult(account.Email, "available", "cancelled", "任务已停止，账号可再次使用", "")
			accounts[i].Password = ""
			continue
		}
		accountCtx, cancel := context.WithTimeout(ctx, loginTTL)
		task.mu.Lock()
		task.view.Current = i + 1
		task.view.Email = account.Email
		task.cancelAccount = cancel
		task.mu.Unlock()
		report := func(phase, message string) {
			// Never allow credential-bearing errors into persistent status or logs.
			if account.Password != "" {
				message = strings.ReplaceAll(message, account.Password, "[redacted]")
			}
			status := "running"
			if phase == "action_required" {
				status = "action_required"
			}
			s.Store.SetGoogleAccountResult(account.Email, status, phase, message, "")
			task.mu.Lock()
			task.view.Phase = phase
			task.view.Message = message
			task.view.Logs = append(task.view.Logs, account.Email+" · "+message)
			if len(task.view.Logs) > 200 {
				task.view.Logs = append([]string{}, task.view.Logs[len(task.view.Logs)-200:]...)
			}
			task.mu.Unlock()
		}
		report("starting", "正在启动 Google 登录")
		login := s.googleLogin
		if login == nil {
			login = s.loginGoogle
		}
		creds, err := login(accountCtx, googleauth.Credential{Email: account.Email, Password: account.Password}, report)
		var accountID string
		if err == nil {
			report("validating", "已完成登录，正在检查 DeepAI Code 资格")
			accountID, err = s.enrollGoogle(accountCtx, account.Email, creds)
		}
		phase, message, status := "done", "授权完成，已加入 DeepAI 账号池", "authorized"
		if err == nil {
			if a := s.Store.AccountCopy(accountID); a != nil && a.Theos != nil && a.Theos.Balance != "" {
				message += "；当前余额 " + a.Theos.Balance + " Theos"
				if !positiveQuota(a.Theos) {
					message += "（当前无可用额度）"
				}
			}
		}
		task.mu.Lock()
		task.cancelAccount = nil
		if err == nil {
			task.view.Succeeded++
		} else if ctx.Err() != nil {
			phase, message, status = "cancelled", "任务已停止，账号可再次使用", "available"
		} else if errors.Is(err, context.Canceled) {
			phase, message, status = "skipped", "已跳过，可勾选重试失败账号重新授权", "failed"
			task.view.Skipped++
		} else {
			phase, message, status = "failed", googleLoginError(err), "failed"
			task.view.Failed++
		}
		task.mu.Unlock()
		report(phase, message)
		s.Store.SetGoogleAccountResult(account.Email, status, phase, message, accountID)
		cancel()
		accounts[i].Password = ""
		if err := s.Store.Save(); err != nil {
			task.mu.Lock()
			task.view.Message = "状态文件保存失败，请检查磁盘后重新导入或授权"
			task.view.Logs = append(task.view.Logs, task.view.Message)
			task.mu.Unlock()
			task.cancel()
		}
	}
	_ = s.Store.Save()
	task.mu.Lock()
	task.view.Status = "completed"
	if ctx.Err() != nil {
		task.view.Status = "cancelled"
	}
	task.mu.Unlock()
}

func googleLoginError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "登录超时，请检查浏览器中的验证要求后重试"
	}
	var upstream *deepai.HTTPError
	if errors.As(err, &upstream) {
		if upstream.Code() == "social_login_required" {
			return "DeepAI 仍要求 Google / Apple 登录，此账号未通过 Code 资格检查"
		}
		return fmt.Sprintf("DeepAI 拒绝了资格检查或密钥交换（HTTP %d），请重试", upstream.Status)
	}
	// Browser errors contain only fixed, locally-generated diagnostic messages.
	return err.Error()
}

func (s *Server) loginGoogle(ctx context.Context, credential googleauth.Credential, report func(string, string)) (*deepai.Credentials, error) {
	session, err := s.createLoginSession()
	if err != nil {
		return nil, err
	}
	defer func() { s.Login.remove(session.ID); shutdownLoginServer(session.srv) }()
	browser, err := googleauth.NewBrowserFromEnv()
	if err != nil {
		return nil, err
	}
	err = browser.Run(ctx, s.Up.SignInURL(session.State, session.Port), credential, func() bool { return session.snapshotResult() != nil }, report)
	if err != nil {
		return nil, err
	}
	result := session.snapshotResult()
	if result == nil || result.Creds == nil {
		return nil, errors.New("DeepAI 登录回调失败，请重新授权")
	}
	return result.Creds, nil
}

func (s *Server) enrollGoogle(ctx context.Context, email string, creds *deepai.Credentials) (string, error) {
	acct, err := deepai.AccountFromCredentials(creds, "google")
	if err != nil {
		return "", err
	}
	acct.Email = email
	acct.Name = email
	acct.Group = "default"
	acct.EmailVerified = true
	acct.LastVerified = time.Now().Unix()
	acct.MachineID, err = s.Store.EnsureGoogleMachineID(email, acct.APIKey)
	if err != nil {
		return "", err
	}
	if err := s.Store.Save(); err != nil {
		return "", errors.New("无法保存 Google 账号设备标识，未查询配额")
	}
	al, err := s.Up.Theos(ctx, acct)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.Store.UpsertAccount(acct)
	s.Store.SetAccountAllowance(acct.ID, al, nil)
	return acct.ID, nil
}
