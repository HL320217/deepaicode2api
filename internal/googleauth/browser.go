package googleauth

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

//go:embed browser.js
var browserScript string

var ErrInteractionRequired = errors.New("Google 无头登录需要人工验证或设置确认；请在有界面环境完成授权后导入 DeepAI Key，或通过虚拟桌面重试")
var ErrBrowserRejected = errors.New("Google 已拒绝此自动化浏览器：此浏览器或应用可能不安全；请在运行网关的电脑上通过控制台“账号池 → 浏览器登录”在普通浏览器授权，Linux 服务可导入已授权的 DeepAI Key")
var ErrSignInRejected = errors.New("Google 已拒绝本次登录；请在普通浏览器查看具体原因，程序不会自动重试拒绝页")
var ErrGoogleServerError = errors.New("Google 登录页面返回服务器错误（5xx），本次未完成授权；请稍后重试")

type Browser struct {
	Executable          string
	Proxy               string
	Headless            bool
	googleOriginForTest string
}

func FindBrowser() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("DEEPAI_GOOGLE_BROWSER")); configured != "" {
		if path, err := exec.LookPath(configured); err == nil {
			return path, nil
		}
		return "", errors.New("DEEPAI_GOOGLE_BROWSER 指向的浏览器不存在")
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome", "msedge", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	for _, root := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
		if root == "" {
			continue
		}
		for _, relative := range []string{"Google/Chrome/Application/chrome.exe", "Microsoft/Edge/Application/msedge.exe"} {
			path := filepath.Join(root, filepath.FromSlash(relative))
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
	}
	return "", errors.New("未找到 Chrome / Edge；请在运行网关的电脑安装浏览器，或设置 DEEPAI_GOOGLE_BROWSER")
}

func isGoogleOrigin(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() == "accounts.google.com" && (u.Port() == "" || u.Port() == "443") && u.User == nil
}

// Run uses a fresh temporary browser profile per account. No existing browser
// cookies or other users' Google sessions are attached to this automation.
func (b *Browser) Run(ctx context.Context, signInURL string, credential Credential, done func() bool, progress func(string, string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateProxy(b.Proxy); err != nil {
		return err
	}
	exe := b.Executable
	if exe == "" {
		var err error
		exe, err = FindBrowser()
		if err != nil {
			return err
		}
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(exe), chromedp.Flag("headless", b.Headless))
	if b.Proxy != "" {
		opts = append(opts, chromedp.ProxyServer(b.Proxy))
	}
	allocator, closeAllocator := chromedp.NewExecAllocator(ctx, opts...)
	defer closeAllocator()
	browser, closeBrowser := chromedp.NewContext(allocator)
	defer closeBrowser()
	if b.Headless {
		progress("browser", "正在启动独立无头浏览器（不需要桌面）")
	} else {
		progress("browser", "正在打开独立浏览器")
	}
	if err := chromedp.Run(browser, chromedp.Navigate(signInURL)); err != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return errors.New("无法打开登录浏览器；请检查浏览器安装、桌面环境与网络")
	}
	contexts := map[target.ID]context.Context{}
	attempts := map[string]bool{}
	lastProgress := ""
	var externalURL string
	var externalSince time.Time
	report := func(phase, message string) {
		if phase+message != lastProgress {
			lastProgress = phase + message
			progress(phase, message)
		}
	}
	ticker := time.NewTicker(900 * time.Millisecond)
	defer ticker.Stop()
	for {
		if done() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		infos, err := chromedp.Targets(browser)
		if err != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return errors.New("登录浏览器已关闭，请重新启动任务")
		}
		// Prefer Google's popup over the waiting DeepAI relay tab.
		var selected *target.Info
		var external string
		for _, info := range infos {
			if info.Type != "page" {
				continue
			}
			if isGoogleOrigin(info.URL) || (b.googleOriginForTest != "" && strings.HasPrefix(info.URL, b.googleOriginForTest+"/")) {
				selected = info
				break
			}
			if strings.HasPrefix(info.URL, "https://deepai.org/") || strings.HasPrefix(info.URL, "https://api.deepai.org/") {
				selected = info
			}
			if info.URL != "about:blank" && !strings.HasPrefix(info.URL, "http://127.0.0.1:") && !strings.HasPrefix(info.URL, "https://deepai.org/") && !strings.HasPrefix(info.URL, "https://api.deepai.org/") {
				external = info.URL
			}
		}
		hasGoogle := selected != nil && (isGoogleOrigin(selected.URL) || (b.googleOriginForTest != "" && strings.HasPrefix(selected.URL, b.googleOriginForTest+"/")))
		if external != "" && !hasGoogle {
			report("action_required", "请在浏览器完成企业 SSO 或额外身份验证，完成后会自动继续")
			if external != externalURL {
				externalURL, externalSince = external, time.Now()
			}
			// Allow transient redirects to finish before treating a stable
			// external page as an inaccessible human-login step.
			if b.Headless && time.Since(externalSince) >= 5*time.Second {
				return ErrInteractionRequired
			}
		} else {
			externalURL, externalSince = "", time.Time{}
		}
		if selected == nil {
			continue
		}
		page := contexts[selected.TargetID]
		if page == nil {
			var cancel context.CancelFunc
			page, cancel = chromedp.NewContext(browser, chromedp.WithTargetID(selected.TargetID))
			defer cancel()
			// Target event handling must inherit the account lifetime. Initializing
			// it under a per-step timeout would stop that event loop on cancellation.
			if err := chromedp.Run(page); err != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				continue
			}
			contexts[selected.TargetID] = page
		}
		stepCtx, cancel := context.WithTimeout(page, 5*time.Second)
		err = b.step(stepCtx, credential, attempts, report)
		cancel()
		if errors.Is(err, ErrInteractionRequired) || errors.Is(err, ErrBrowserRejected) || errors.Is(err, ErrSignInRejected) || errors.Is(err, ErrGoogleServerError) {
			return err
		}
		if err != nil && ctx.Err() == nil {
			report("action_required", "页面正在跳转或需要确认，请查看登录浏览器；程序会继续检测")
		}
	}
}

func (b *Browser) step(ctx context.Context, credential Credential, attempted map[string]bool, report func(string, string)) error {
	var location string
	if err := chromedp.Run(ctx, chromedp.Location(&location)); err != nil {
		return err
	}
	u, err := url.Parse(location)
	if err != nil {
		return err
	}
	mode := ""
	if isGoogleOrigin(location) || (b.googleOriginForTest != "" && u.Scheme+"://"+u.Host == b.googleOriginForTest) {
		mode = "google"
	}
	if u.Scheme == "https" && u.User == nil && (u.Host == "deepai.org" || u.Host == "api.deepai.org") {
		mode = "deepai"
	}
	if mode == "" {
		return nil
	}
	// DeepAI pages never receive Google credentials, even as script arguments.
	args := map[string]any{"mode": mode, "origin": u.Scheme + "://" + u.Host, "attempted": attempted}
	if mode == "google" {
		args["email"] = credential.Email
		args["password"] = credential.Password
	}
	encoded, _ := json.Marshal(args)
	var result struct {
		Phase               string `json:"phase"`
		Message             string `json:"message"`
		Submitted           string `json:"submitted"`
		InteractionRequired bool   `json:"interactionRequired"`
		TerminalError       string `json:"terminalError"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate("("+browserScript+")("+string(encoded)+")", &result, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithUserGesture(true) })); err != nil {
		return err
	}
	if result.Submitted != "" {
		attempted[result.Submitted] = true
	}
	if result.TerminalError == "browser_rejected" {
		report("browser_rejected", ErrBrowserRejected.Error())
		return ErrBrowserRejected
	}
	if result.TerminalError == "signin_rejected" {
		report("signin_rejected", ErrSignInRejected.Error())
		return ErrSignInRejected
	}
	if result.TerminalError == "google_server_error" {
		report("google_server_error", ErrGoogleServerError.Error())
		return ErrGoogleServerError
	}
	if b.Headless && result.InteractionRequired {
		report("action_required", ErrInteractionRequired.Error())
		return ErrInteractionRequired
	}
	if result.Phase != "" {
		report(result.Phase, result.Message)
	}
	return nil
}
