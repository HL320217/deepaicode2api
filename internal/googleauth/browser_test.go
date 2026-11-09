package googleauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Opt-in network smoke check: opens the real DeepAI Google OAuth entry without
// entering any account credentials, signing in or creating an account.
func TestLiveDeepAIGoogleEntry(t *testing.T) {
	if os.Getenv("DEEPAI_GOOGLE_LIVE_TEST") != "1" {
		t.Skip("set DEEPAI_GOOGLE_LIVE_TEST=1 to check the official OAuth entry")
	}
	exe, err := FindBrowser()
	if err != nil {
		t.Fatal(err)
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(exe), chromedp.Headless, chromedp.NoSandbox)
	if proxy := os.Getenv("HTTPS_PROXY"); proxy != "" {
		opts = append(opts, chromedp.ProxyServer(proxy))
	}
	allocator, stopAllocator := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stopAllocator()
	browser, stopBrowser := chromedp.NewContext(allocator)
	defer stopBrowser()
	ctx, cancel := context.WithTimeout(browser, 40*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Navigate("https://api.deepai.org/deepai_code/desktop_auth/?state="+strings.Repeat("a", 64)+"&port=12345")); err != nil {
		t.Fatal(err)
	}
	b := &Browser{}
	attempted := map[string]bool{}
	for {
		targets, err := chromedp.Targets(browser)
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range targets {
			if target.Type == "page" && isGoogleOrigin(target.URL) {
				t.Log("DeepAI official OAuth opened accounts.google.com; no credentials submitted")
				return
			}
		}
		if err := b.step(ctx, Credential{}, attempted, func(string, string) {}); err != nil && ctx.Err() != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("DeepAI Google entry did not open before timeout")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func TestBrowserRunsSuccessiveGooglePages(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	var done atomic.Bool
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/done" {
			done.Store(true)
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<input id="identifierId" type="email"><button id="identifierNext" onclick="showPassword()">Next</button><script>function showPassword(){document.body.innerHTML='<input name="Passwd" type="password"><button id="passwordNext" onclick="fetch(\'/done\')">Next</button>';}</script>`))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	b := &Browser{Executable: exe, googleOriginForTest: fixture.URL, Headless: true}
	err = b.Run(ctx, fixture.URL, Credential{Email: "a@school.edu", Password: "fixture-password"}, done.Load, func(string, string) {})
	if err != nil || !done.Load() {
		t.Fatalf("successive steps failed: %v", err)
	}
}

// The first-use Workspace notice and the optional setup screen are separate
// steps. Neither should leave the account waiting for manual intervention.
func TestBrowserRunsWorkspaceOnboarding(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	var done atomic.Bool
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/v3/signin/speedbump/workspacetermsofservice":
			_, _ = w.Write([]byte(`<h1>欢迎使用您的新账号</h1><p>Google Workspace</p><button onclick="location.href='/v3/signin/speedbump/passkeyenrollment'">我了解</button>`))
		case "/v3/signin/speedbump/passkeyenrollment":
			_, _ = w.Write([]byte(`<h1>简化登录流程</h1><button onclick="document.body.dataset.changed='true'">创建通行密钥</button><button onclick="location.href='/done'">以后再说</button>`))
		case "/done":
			done.Store(true)
			_, _ = w.Write([]byte(`Done`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	b := &Browser{Executable: exe, googleOriginForTest: fixture.URL, Headless: true}
	err = b.Run(ctx, fixture.URL+"/v3/signin/speedbump/workspacetermsofservice", Credential{Email: "a@school.edu", Password: "fixture-password"}, done.Load, func(string, string) {})
	if err != nil || !done.Load() {
		t.Fatalf("Workspace notice and optional setup did not complete: %v", err)
	}
}

func TestBrowserOnboardingOnlyClicksExpectedActions(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<h1>Google</h1><p>` + r.URL.Query().Get("text") + `</p>` + r.URL.Query().Get("input") + `<button onclick="document.body.dataset.clicks=Number(document.body.dataset.clicks || 0)+1">` + r.URL.Query().Get("label") + `</button>`))
	}))
	defer fixture.Close()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(exe), chromedp.Headless, chromedp.NoSandbox)
	allocator, stopAllocator := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stopAllocator()
	ctx, stopBrowser := chromedp.NewContext(allocator)
	defer stopBrowser()
	ctx, deadline := context.WithTimeout(ctx, 25*time.Second)
	defer deadline()
	b := &Browser{googleOriginForTest: fixture.URL}
	for _, tc := range []struct {
		name, path, label, text, input string
		click                          bool
	}{
		{name: "English Workspace notice", path: "/v3/signin/speedbump/workspacetermsofservice", label: "I understand", click: true},
		{name: "English optional setup", path: "/v3/signin/speedbump/passkeyenrollment", label: "Not now", click: true},
		{name: "unrelated notice", path: "/unrelated", label: "我了解"},
		{name: "unrelated optional button", path: "/unrelated", label: "以后再说"},
		{name: "required password change", path: "/v3/signin/speedbump/changepassword", label: "继续", text: "Sign in to DeepAI"},
		{name: "password change form", path: "/v3/signin/speedbump/changepassword", label: "继续", input: `<input name="Passwd" type="password"><button id="passwordNext" onclick="document.body.dataset.clicks=Number(document.body.dataset.clicks || 0)+1">Next</button>`},
		{name: "optional setup must not enable feature", path: "/v3/signin/speedbump/passkeyenrollment", label: "创建通行密钥", text: "Sign in to DeepAI"},
		{name: "optional recovery phone", path: "/v3/signin/speedbump/phonecollection", label: "Not now", text: "Add recovery phone", input: `<input type="tel">`, click: true},
		{name: "optional passkey reminder", path: "/v3/signin/speedbump/passkeyenrollment", label: "以后再说", text: "验证您的身份更加简单", click: true},
		{name: "actual phone challenge", path: "/v3/signin/challenge/ipp", label: "以后再说", text: "Verify it's you", input: `<input type="tel">`},
		{name: "unknown speedbump", path: "/v3/signin/speedbump/unknown", label: "Not now"},
		{name: "setup with actual verification code", path: "/v3/signin/speedbump/phonecollection", label: "Not now", input: `<input name="idvPin">`},
		{name: "DeepAI consent", path: "/signin/oauth/id", label: "Continue", text: "Sign in to DeepAI", click: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{"label": {tc.label}, "text": {tc.text}, "input": {tc.input}}
			if err := chromedp.Run(ctx, chromedp.Navigate(fixture.URL+tc.path+"?"+query.Encode())); err != nil {
				t.Fatal(err)
			}
			attempted := map[string]bool{}
			for range 2 {
				if err := b.step(ctx, Credential{}, attempted, func(string, string) {}); err != nil {
					t.Fatal(err)
				}
			}
			var clicks int
			if err := chromedp.Run(ctx, chromedp.Evaluate(`Number(document.body.dataset.clicks || 0)`, &clicks)); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.click {
				want = 1
			}
			if clicks != want {
				t.Fatalf("button clicks = %d, want %d", clicks, want)
			}
		})
	}
}

func TestBrowserPreservesCancellation(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = (&Browser{Executable: exe}).Run(ctx, "https://deepai.org/", Credential{}, func() bool { return false }, func(string, string) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestHeadlessBrowserReturnsWhenHumanVerificationIsRequired(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<h1>2-Step Verification</h1><input name="totpPin">`))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	b := &Browser{Executable: exe, Headless: true, googleOriginForTest: fixture.URL}
	err = b.Run(ctx, fixture.URL, Credential{}, func() bool { return false }, func(string, string) {})
	if errors.Is(err, context.DeadlineExceeded) || err == nil || !strings.Contains(err.Error(), "无头") {
		t.Fatalf("headless browser waited for invisible human intervention: %v", err)
	}
}

func TestBrowserDetectsTerminalRejectionWithoutSubmittingOrRetrying(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<p>` + r.URL.Query().Get("message") + `</p><input id="identifierId" type="email"><input name="Passwd" type="password"><button id="identifierNext" onclick="document.body.dataset.clicked='true'">Next</button><button onclick="document.body.dataset.clicked='true'">重试</button>`))
	}))
	defer fixture.Close()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(exe), chromedp.Headless)
	allocator, stopAllocator := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stopAllocator()
	ctx, stopBrowser := chromedp.NewContext(allocator)
	defer stopBrowser()
	ctx, deadline := context.WithTimeout(ctx, 20*time.Second)
	defer deadline()
	for _, tc := range []struct{ name, path, message, want string }{
		{"Chinese browser rejection", "/v3/signin/rejected", "此浏览器或应用可能不安全。", "不安全"},
		{"English browser rejection", "/v3/signin/challenge/pwd", "This browser or app may not be secure.", "不安全"},
		{"localized rejection route", "/v3/signin/rejected", "Access denied", "拒绝"},
	} {
		for _, headless := range []bool{false, true} {
			t.Run(tc.name+"/headless="+strconv.FormatBool(headless), func(t *testing.T) {
				if err := chromedp.Run(ctx, chromedp.Navigate(fixture.URL+tc.path+"?message="+url.QueryEscape(tc.message))); err != nil {
					t.Fatal(err)
				}
				b := &Browser{Headless: headless, googleOriginForTest: fixture.URL}
				attempted := map[string]bool{}
				err := b.step(ctx, Credential{Email: "a@school.edu", Password: "fixture-secret"}, attempted, func(string, string) {})
				if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "fixture-secret") {
					t.Fatalf("rejection was not classified correctly: %v", err)
				}
				var changed bool
				if err := chromedp.Run(ctx, chromedp.Evaluate(`!!document.body.dataset.clicked || Array.from(document.querySelectorAll('input')).some(el => el.value !== '')`, &changed)); err != nil {
					t.Fatal(err)
				}
				if changed || len(attempted) != 0 {
					t.Fatal("login rejection submitted credentials or clicked retry")
				}
			})
		}
	}
}

func TestBrowserRunStopsOnGoogleRejection(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<h1>Couldn't sign you in</h1><p>This browser or app may not be secure.</p>`))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := &Browser{Executable: exe, Headless: true, googleOriginForTest: fixture.URL}
	err = b.Run(ctx, fixture.URL+"/v3/signin/rejected", Credential{}, func() bool { return false }, func(string, string) {})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "无头登录需要人工") || !strings.Contains(err.Error(), "不安全") {
		t.Fatalf("browser rejection did not stop with the actual cause: %v", err)
	}
}

func TestBrowserRunStopsOnGoogleServerError(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<title>Error 500 (服务器错误)!!1</title><p>500. 出现了错误。</p><p>发生错误。请稍后重试。</p>`))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	b := &Browser{Executable: exe, Headless: true, googleOriginForTest: fixture.URL}
	err = b.Run(ctx, fixture.URL+"/v3/signin/speedbump/workspacetermsofservice", Credential{}, func() bool { return false }, func(string, string) {})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "服务器错误") {
		t.Fatalf("Google server error was left waiting for setup: %v", err)
	}
}

func TestHeadlessBrowserStopsWhenAcknowledgementDoesNotNavigate(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<h1>Google Workspace</h1><button onclick="document.body.dataset.clicks=Number(document.body.dataset.clicks || 0)+1">我了解</button>`))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := &Browser{Executable: exe, Headless: true, googleOriginForTest: fixture.URL}
	err = b.Run(ctx, fixture.URL+"/v3/signin/speedbump/workspacetermsofservice", Credential{}, func() bool { return false }, func(string, string) {})
	if !errors.Is(err, ErrInteractionRequired) {
		t.Fatalf("acknowledged setup did not stop after its navigation stalled: %v", err)
	}
}

func TestHeadlessBrowserWaitsForWorkspaceButton(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	var done atomic.Bool
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/done" {
			done.Store(true)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<h1>Google Workspace</h1><script>setTimeout(()=>{document.body.innerHTML += '<button onclick="location.href=\'/done\'">我了解</button>'}, 2000)</script>`))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	b := &Browser{Executable: exe, Headless: true, googleOriginForTest: fixture.URL}
	err = b.Run(ctx, fixture.URL+"/v3/signin/speedbump/workspacetermsofservice", Credential{}, done.Load, func(string, string) {})
	if err != nil || !done.Load() {
		t.Fatalf("headless login gave up before Workspace button rendered: %v", err)
	}
}

func TestHeadlessBrowserStopsOnExternalSSO(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<h1>Enterprise SSO</h1><input type="password">`))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := &Browser{Executable: exe, Headless: true}
	// The alternate hostname represents an untrusted SSO origin rather than the
	// loopback callback endpoint or the configured Google fixture origin.
	err = b.Run(ctx, strings.Replace(fixture.URL, "127.0.0.1", "localhost", 1), Credential{}, func() bool { return false }, func(string, string) {})
	if errors.Is(err, context.DeadlineExceeded) || err == nil || !strings.Contains(err.Error(), "无头") {
		t.Fatalf("headless login waited for an inaccessible SSO screen: %v", err)
	}
}

func TestHeadlessBrowserStopsWhenSetupCannotBeSkipped(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<h1>Set up a passkey</h1><button>Create a passkey</button>`))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := &Browser{Executable: exe, Headless: true, googleOriginForTest: fixture.URL}
	err = b.Run(ctx, fixture.URL+"/v3/signin/speedbump/passkeyenrollment", Credential{}, func() bool { return false }, func(string, string) {})
	if errors.Is(err, context.DeadlineExceeded) || err == nil || !strings.Contains(err.Error(), "无头") {
		t.Fatalf("headless login waited for mandatory setup: %v", err)
	}
}

func TestGoogleOriginIsExact(t *testing.T) {
	for _, raw := range []string{"https://accounts.google.com/signin", "https://accounts.google.com:443/signin"} {
		if !isGoogleOrigin(raw) {
			t.Errorf("rejected Google origin %s", raw)
		}
	}
	for _, raw := range []string{"http://accounts.google.com/", "https://accounts.google.com.evil.test/", "https://evil.test/accounts.google.com", "https://accounts.google.com:444/", "https://user@accounts.google.com/"} {
		if isGoogleOrigin(raw) {
			t.Errorf("unsafe credential origin %s", raw)
		}
	}
}

func TestBrowserCredentialEntryAndOriginBoundary(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<input id="identifierId" type="email"><button id="identifierNext" onclick="document.body.dataset.submitted=document.querySelector('input').value">Next</button>`))
	}))
	defer fixture.Close()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(exe), chromedp.Headless, chromedp.NoSandbox)
	allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAllocator()
	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 20*time.Second)
	defer deadline()
	if err := chromedp.Run(ctx, chromedp.Navigate(fixture.URL)); err != nil {
		t.Fatal(err)
	}
	credentials := Credential{Email: "test@school.edu", Password: "never-on-this-origin"}
	browser := &Browser{}
	if err := browser.step(ctx, credentials, map[string]bool{}, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := chromedp.Run(ctx, chromedp.Value("#identifierId", &value)); err != nil {
		t.Fatal(err)
	}
	if value != "" {
		t.Fatal("credentials were entered on an unrelated origin")
	}
	// The origin override is private to this package and used only by fixtures.
	browser.googleOriginForTest = fixture.URL
	if err := browser.step(ctx, credentials, map[string]bool{}, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	var submitted string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body.dataset.submitted`, &submitted)); err != nil {
		t.Fatal(err)
	}
	if submitted != credentials.Email {
		t.Fatal("email was not submitted")
	}
	// Password entry is a separate Google page; a challenge never submits it.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body.innerHTML='<input name="Passwd" type="password"><button id="passwordNext" onclick="document.body.dataset.submitted=document.querySelector(\'input\').value">Next</button>'`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := browser.step(ctx, credentials, map[string]bool{}, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body.dataset.submitted`, &submitted)); err != nil {
		t.Fatal(err)
	}
	if submitted != credentials.Password {
		t.Fatal("password was not submitted")
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body.innerHTML='<h1>2-Step Verification</h1><input type="tel">'`, nil)); err != nil {
		t.Fatal(err)
	}
	var phase string
	if err := browser.step(ctx, credentials, map[string]bool{}, func(p, m string) {
		phase = p
		if strings.Contains(m, credentials.Password) {
			t.Fatal("password leak")
		}
	}); err != nil {
		t.Fatal(err)
	}
	if phase != "action_required" {
		t.Fatalf("challenge phase = %s", phase)
	}
}
