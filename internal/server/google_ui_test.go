package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/googleauth"
	"github.com/chromedp/chromedp"
)

// This exercises the actual embedded console, file upload and skip/cancel APIs
// in Chromium. Only the external Google authentication is replaced by a fixture.
func TestGoogleConsoleImportAndTaskControls(t *testing.T) {
	exe, err := googleauth.FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	s := newEmptyServer(t)
	s.googleLogin = func(ctx context.Context, c googleauth.Credential, report func(string, string)) (*deepai.Credentials, error) {
		report("action_required", "请在浏览器完成双重验证")
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var delayStatus atomic.Bool
	staleStatusReady, releaseStatus := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStatus) }) }
	defer release()
	handler := s.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/api/google/status" && delayStatus.CompareAndSwap(true, false) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			close(staleStatusReady)
			<-releaseStatus
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "accounts.txt")
	if err := os.WriteFile(file, []byte("first@school.edu----test-google-password\nsecond@company.org----another-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(exe), chromedp.Headless, chromedp.NoSandbox, chromedp.WindowSize(1440, 1100))
	allocator, stopAllocator := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stopAllocator()
	browser, stopBrowser := chromedp.NewContext(allocator)
	defer stopBrowser()
	ctx, cancel := context.WithTimeout(browser, 35*time.Second)
	defer cancel()
	defer func() { googleRequest(t, s, "POST", "/admin/api/google/cancel", `{}`) }()
	if err := chromedp.Run(ctx,
		chromedp.Navigate(server.URL),
		chromedp.Click("#loginSubmit", chromedp.ByQuery),
		chromedp.WaitNotPresent("body.signed-out", chromedp.ByQuery),
		chromedp.Click(`nav button[data-tab="register"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.google-import').open=true`, nil),
		chromedp.SetUploadFiles("#googleFile", []string{file}, chromedp.ByQuery),
		chromedp.Click("#googleImport", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#googleImportStatus').textContent.includes('新增 2')`, nil),
		chromedp.Poll(`!document.querySelector('#googleImport').disabled`, nil),
	); err != nil {
		t.Fatal(err)
	}
	// A pre-start refresh is deliberately delayed until the start request has
	// completed. The console must discard that stale idle state and fetch again.
	delayStatus.Store(true)
	if err := chromedp.Run(ctx, chromedp.Click("#googleReload", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-staleStatusReady:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := chromedp.Run(ctx,
		chromedp.SetValue("#googleCount", "2", chromedp.ByQuery),
		chromedp.Click("#googleStart", chromedp.ByQuery),
		chromedp.Poll(`performance.getEntriesByType('resource').some(e => e.name.endsWith('/admin/api/google/start'))`, nil),
	); err != nil {
		t.Fatal(err)
	}
	release()
	if err := chromedp.Run(ctx,
		chromedp.Poll(`document.querySelector('#googleStatus').textContent.includes('first@school.edu') && document.querySelector('#googleStatus').textContent.includes('双重验证')`, nil),
	); err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("DEEPAI_UI_SCREENSHOTS"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, theme := range []string{"light", "dark"} {
			var screenshot []byte
			if err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.dataset.theme=`+`"`+theme+`"`, nil), chromedp.FullScreenshot(&screenshot, 85)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "google-pool-"+theme+".png"), screenshot, 0600); err != nil {
				t.Fatal(err)
			}
		}
		var screenshot []byte
		if err := chromedp.Run(ctx, chromedp.EmulateViewport(390, 844), chromedp.FullScreenshot(&screenshot, 85)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "google-pool-mobile.png"), screenshot, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := chromedp.Run(ctx,
		chromedp.Click("#googleSkip", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#googleStatus').textContent.includes('second@company.org')`, nil),
		chromedp.Click("#googleCancel", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#googleStatus').textContent.includes('任务已停止')`, nil),
	); err != nil {
		t.Fatal(err)
	}
	var leaked bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body.innerText.includes('test-google-password') || document.body.innerText.includes('another-password')`, &leaked)); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("Google credentials were exposed in console")
	}
	pool := s.Store.GoogleAccounts()
	if len(pool) != 2 || pool[0].Phase != "skipped" || pool[1].Status != "available" {
		t.Fatalf("incorrect final pool: %+v", pool)
	}
}
