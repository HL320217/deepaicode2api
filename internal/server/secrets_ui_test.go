package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"deepaicode2api/internal/deepai"
	"github.com/chromedp/chromedp"
)

func TestCompactConsoleSecretControls(t *testing.T) {
	s, key := newTestServer(t, nil)
	for i := 0; i < 8; i++ {
		s.Store.UpsertAccount(&deepai.Account{ID: string(rune('a' + i)), Name: "演示账号", Email: string(rune('a'+i)) + "@example.invalid", APIKey: "fixture-" + string(rune('a'+i)), Enabled: true, Status: "ok"})
	}
	var reveals atomic.Int32
	handler := s.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/api/secrets" {
			reveals.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx := resourceRegressionBrowser(t)
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1440, 900), chromedp.Navigate(server.URL), chromedp.Click("#loginSubmit", chromedp.ByQuery), chromedp.WaitNotPresent("body.signed-out", chromedp.ByQuery),
		chromedp.Click(`nav button[data-tab="accounts"]`, chromedp.ByQuery), chromedp.WaitVisible(`[data-account-id="acc-1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	var compact bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#accounts tr').getBoundingClientRect().top < 270 && !document.querySelector('#tab-accounts .resource-heading p')`, &compact)); err != nil {
		t.Fatal(err)
	}
	if !compact {
		t.Fatal("account toolbar should leave the first record visible within 270px of the viewport top")
	}
	if reveals.Load() != 0 {
		t.Fatal("loading a list must not request secrets")
	}
	if dir := os.Getenv("DEEPAI_UI_SCREENSHOTS"); dir != "" {
		var screenshot []byte
		if err := chromedp.Run(ctx, chromedp.FullScreenshot(&screenshot, 90)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "compact-accounts.png"), screenshot, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`[data-account-id="acc-1"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-secret-reveal="account"]`, chromedp.ByQuery),
		chromedp.Click(`[data-secret-reveal="account"]`, chromedp.ByQuery), chromedp.Poll(`document.querySelector('[data-secret-value="account"]').textContent==='upstream-key'`, nil),
		chromedp.Click(`[data-secret-reveal="account"]`, chromedp.ByQuery), chromedp.Poll(`document.querySelector('[data-secret-value="account"]').textContent!=='upstream-key'`, nil),
		chromedp.Evaluate(`Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>{window.fixtureCopied=text}}})`, nil),
		chromedp.Click(`[data-secret-copy="account"]`, chromedp.ByQuery), chromedp.Poll(`window.fixtureCopied==='upstream-key'`, nil),
		chromedp.Click("#resourceClose", chromedp.ByQuery),
		chromedp.Click(`nav button[data-tab="keys"]`, chromedp.ByQuery), chromedp.Click("#keys .resource-name", chromedp.ByQuery),
		chromedp.Click(`[data-secret-reveal="key"]`, chromedp.ByQuery), chromedp.Poll(`document.querySelector('[data-secret-reveal="key"]').getAttribute('aria-expanded')==='true'`, nil),
	); err != nil {
		t.Fatal(err)
	}
	var shown string
	if err := chromedp.Run(ctx, chromedp.Text(`[data-secret-value="key"]`, &shown, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if shown != key {
		t.Fatal("key detail did not reveal the actual saved gateway key")
	}
	if err := chromedp.Run(ctx, chromedp.Click("#resourceClose", chromedp.ByQuery), chromedp.Click("#keys .resource-name", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var masked bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('[data-secret-reveal="key"]').getAttribute('aria-expanded')==='false'`, &masked)); err != nil {
		t.Fatal(err)
	}
	if !masked {
		t.Fatal("reopened detail must start masked")
	}
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(390, 844), chromedp.Click("#resourceClose", chromedp.ByQuery), chromedp.Click(`nav button[data-tab="accounts"]`, chromedp.ByQuery), chromedp.Evaluate(`document.documentElement.scrollWidth<=innerWidth+1`, &compact)); err != nil {
		t.Fatal(err)
	}
	if !compact {
		t.Fatal("compact controls overflow mobile viewport")
	}
}
