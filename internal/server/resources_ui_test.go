package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/googleauth"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Exercise the embedded console against real local handlers. All credentials
// belong to disposable fixtures; none of these actions contacts an upstream.
func TestResourcesConsoleEditingBulkActionsAndDrafts(t *testing.T) {
	exe, err := googleauth.FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	s := newEmptyServer(t)
	for _, a := range []*deepai.Account{
		{ID: "acc-1", Name: "Fixture Alpha", Email: "alpha@example.invalid", APIKey: "fixture-upstream-alpha", Enabled: true, Status: "ok", Group: "default"},
		{ID: "acc-2", Name: "Fixture Beta", Email: "beta@example.invalid", APIKey: "fixture-upstream-beta", Enabled: true, Status: "ok", Group: "default"},
	} {
		s.Store.UpsertAccount(a)
	}
	s.Store.AddKey("Fixture development", "default")
	s.Store.AddKey("Fixture production", "production")
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(exe), chromedp.Headless, chromedp.NoSandbox, chromedp.WindowSize(1440, 1100))
	allocator, stopAllocator := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stopAllocator()
	browser, stopBrowser := chromedp.NewContext(allocator)
	defer stopBrowser()
	ctx, cancel := context.WithTimeout(browser, 90*time.Second)
	defer cancel()
	// Allocate the browser under the test lifetime, not a single stage's context.
	if err := chromedp.Run(ctx); err != nil {
		t.Fatal(err)
	}
	run := func(stage string, actions ...chromedp.Action) {
		t.Helper()
		stageCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		if err := chromedp.Run(stageCtx, actions...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	assertJS := func(stage, expression string) {
		t.Helper()
		var ok bool
		run(stage, chromedp.Evaluate(expression, &ok))
		if !ok {
			t.Fatalf("%s: condition failed: %s", stage, expression)
		}
	}
	// Dispatch the same events used by typing, including an empty search value.
	input := func(selector, value string) chromedp.Action {
		selJSON, _ := json.Marshal(selector)
		valueJSON, _ := json.Marshal(value)
		return chromedp.Evaluate(`(() => { const el = document.querySelector(`+string(selJSON)+`); el.value = `+string(valueJSON)+`; el.dispatchEvent(new Event('input', {bubbles:true})); el.dispatchEvent(new Event('change', {bubbles:true})); })()`, nil)
	}
	run("sign in and open accounts",
		chromedp.Navigate(server.URL),
		chromedp.Click("#loginSubmit", chromedp.ByQuery),
		chromedp.WaitNotPresent("body.signed-out", chromedp.ByQuery),
		chromedp.Click(`nav button[data-tab="accounts"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`button[data-account-id="acc-1"]`, chromedp.ByQuery),
		input("#acctSearch", "alpha"),
		chromedp.Poll(`document.querySelectorAll('[data-select-account]').length === 1`, nil),
		chromedp.Click(`button[data-account-id="acc-1"]`, chromedp.ByQuery),
		chromedp.WaitVisible("#resourceDialog[open]", chromedp.ByQuery),
	)
	assertJS("drawer contains the selected account", `document.querySelector('#editName').value === 'Fixture Alpha'`)
	run("save account details",
		input("#editName", "Fixture Alpha edited"),
		input("#editGroup", "production"),
		chromedp.Click("#resourceSave", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#resourceDialog').open && document.querySelector('#accounts').textContent.includes('Fixture Alpha edited')`, nil),
	)
	a := s.Store.AccountCopy("acc-1")
	if a == nil || a.Name != "Fixture Alpha edited" || a.Group != "production" {
		t.Fatalf("account edit was not persisted: %+v", a)
	}
	assertJS("editing preserves the account search", `document.querySelector('#acctSearch').value === 'alpha' && document.querySelectorAll('[data-select-account]').length === 1`)
	run("Escape closes the native drawer",
		chromedp.Click(`button[data-account-id="acc-1"]`, chromedp.ByQuery),
		chromedp.WaitVisible("#resourceDialog[open]", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Poll(`!document.querySelector('#resourceDialog').open`, nil),
	)
	run("select only the filtered page and disable",
		chromedp.Click("#acctSelectAll", chromedp.ByQuery),
		chromedp.Click("#acctBulkDisable", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#accounts').textContent.includes('已停用')`, nil),
	)
	if s.Store.AccountCopy("acc-1").Enabled || !s.Store.AccountCopy("acc-2").Enabled {
		t.Fatal("bulk disable must affect the selected filtered page only")
	}
	// Observe a completed refresh request before asserting the unsaved value.
	run("preserve settings draft across refresh",
		chromedp.Click(`nav button[data-tab="settings"]`, chromedp.ByQuery),
		input("#sTimeout", "173"),
		chromedp.Evaluate(`performance.clearResourceTimings()`, nil),
		chromedp.Click("#refresh", chromedp.ByQuery),
		chromedp.Poll(`performance.getEntriesByType('resource').some(e => e.name.endsWith('/admin/api/state') && e.responseEnd > 0)`, nil),
	)
	assertJS("settings draft remains unsaved", `document.querySelector('#sTimeout').value === '173'`)
	run("return to the filtered account list",
		chromedp.Click(`nav button[data-tab="accounts"]`, chromedp.ByQuery),
	)
	assertJS("refresh preserves account search", `document.querySelector('#acctSearch').value === 'alpha'`)
	run("show all fixture accounts", input("#acctSearch", ""), chromedp.Poll(`document.querySelectorAll('[data-select-account]').length === 2`, nil))
	if dir := os.Getenv("DEEPAI_UI_SCREENSHOTS"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, theme := range []string{"light", "dark"} {
			var screenshot []byte
			run("capture desktop "+theme,
				chromedp.Evaluate(`document.documentElement.dataset.theme="`+theme+`"`, nil),
				chromedp.FullScreenshot(&screenshot, 85),
			)
			if err := os.WriteFile(filepath.Join(dir, "resources-"+theme+".png"), screenshot, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("resize account page to mobile", chromedp.EmulateViewport(390, 844))
	assertJS("mobile account page has no horizontal body overflow", `Math.max(document.body.scrollWidth, document.documentElement.scrollWidth) <= window.innerWidth + 1`)
	run("open mobile drawer",
		chromedp.Click(`button[data-account-id="acc-1"]`, chromedp.ByQuery),
		chromedp.WaitVisible("#resourceDialog[open]", chromedp.ByQuery),
		chromedp.Poll(`getComputedStyle(document.querySelector('#resourceDialog')).opacity === '1'`, nil),
	)
	var drawerBounds map[string]float64
	run("measure mobile drawer", chromedp.Evaluate(`(() => { const r = document.querySelector('#resourceDialog').getBoundingClientRect(); return {left:r.left,right:r.right,width:r.width,viewport:innerWidth}; })()`, &drawerBounds))
	t.Logf("mobile drawer bounds: %+v", drawerBounds)
	if dir := os.Getenv("DEEPAI_UI_SCREENSHOTS"); dir != "" {
		var screenshot []byte
		run("capture mobile drawer", chromedp.FullScreenshot(&screenshot, 85))
		if err := os.WriteFile(filepath.Join(dir, "resources-mobile.png"), screenshot, 0600); err != nil {
			t.Fatal(err)
		}
	}
	assertJS("mobile drawer fits viewport", `(() => { const r = document.querySelector('#resourceDialog').getBoundingClientRect(); return r.left >= -1 && r.right <= innerWidth + 1; })()`)
	run("close mobile drawer and select all accounts",
		chromedp.Click("#resourceClose", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#resourceDialog').open`, nil),
		chromedp.EmulateViewport(1440, 1100),
		// Normalize any retained partial selection before selecting the whole page.
		chromedp.Evaluate(`document.querySelectorAll('[data-select-account]:checked').forEach(el => el.click())`, nil),
		chromedp.Click("#acctSelectAll", chromedp.ByQuery),
	)
	assertJS("all visible accounts are selected", `document.querySelectorAll('[data-select-account]:checked').length === 2`)
	run("cancel destructive bulk action",
		chromedp.Click("#acctBulkRemove", chromedp.ByQuery),
		chromedp.WaitVisible("#confirmDialog[open]", chromedp.ByQuery),
		chromedp.Click("#confirmCancel", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#confirmDialog').open`, nil),
	)
	if len(s.Store.Accounts()) != 2 {
		t.Fatal("cancelling bulk removal changed the account pool")
	}
	run("confirm destructive bulk action",
		chromedp.Click("#acctBulkRemove", chromedp.ByQuery),
		chromedp.WaitVisible("#confirmDialog[open]", chromedp.ByQuery),
		chromedp.Click("#confirmAccept", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#confirmDialog').open && document.querySelectorAll('[data-select-account]').length === 0`, nil),
	)
	if len(s.Store.Accounts()) != 0 {
		t.Fatal("confirmed bulk removal did not remove both selected accounts")
	}
	if len(s.Store.Keys()) != 2 {
		t.Fatal("account operations unexpectedly modified gateway keys")
	}
}

// A successful creation must reveal its one-time secret even if refreshing the
// surrounding list fails. Retrying must not silently mint another credential.
func TestResourcesConsoleCreatedKeySurvivesRefreshFailure(t *testing.T) {
	s := newEmptyServer(t)
	var keyCreates, rejectedRefreshes atomic.Int32
	var failRefresh atomic.Bool
	handler := s.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/api/state" && failRefresh.Load() {
			rejectedRefreshes.Add(1)
			writeErr(w, http.StatusServiceUnavailable, "fixture refresh unavailable", "server_error")
			return
		}
		if r.URL.Path == "/admin/api/keys" && r.Method == http.MethodPost {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code >= 200 && recorder.Code < 300 {
				keyCreates.Add(1)
				failRefresh.Store(true)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx := resourceRegressionBrowser(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(server.URL),
		chromedp.Click("#loginSubmit", chromedp.ByQuery),
		chromedp.WaitNotPresent("body.signed-out", chromedp.ByQuery),
		chromedp.Click(`nav button[data-tab="keys"]`, chromedp.ByQuery),
		chromedp.Click("#tab-keys .resource-heading button", chromedp.ByQuery),
		chromedp.SetValue("#editName", "Only once fixture", chromedp.ByQuery),
		chromedp.Click("#resourceSave", chromedp.ByQuery),
		chromedp.WaitVisible("#resourceDialog[open] .resource-secret", chromedp.ByQuery),
		chromedp.Poll(`performance.getEntriesByType('resource').filter(e => e.name.endsWith('/admin/api/state')).length >= 3`, nil),
	); err != nil {
		t.Fatal(err)
	}
	var secret string
	var cannotResubmit bool
	if err := chromedp.Run(ctx,
		chromedp.Text("#resourceDialog .resource-secret", &secret, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#resourceSave').hidden`, &cannotResubmit),
	); err != nil {
		t.Fatal(err)
	}
	keys := s.Store.Keys()
	if len(keys) != 1 || keyCreates.Load() != 1 || keys[0].Key != secret || !cannotResubmit {
		t.Fatal("creation did not retain the actual one-time secret in a non-resubmittable success view")
	}
	if rejectedRefreshes.Load() == 0 {
		t.Fatal("refresh failure injection was not exercised")
	}
}

// The oldest poll is held while a newer task becomes current. Its eventual
// response must not replace the current task's logs or restart the old poll.
func TestResourcesConsoleIgnoresStaleRegisterPoll(t *testing.T) {
	s := newEmptyServer(t)
	s.Registers.put(&registerJob{ID: "old-task", StartedAt: time.Now().Unix(), Count: 7, running: true, logs: []string{"OLD TASK LOG"}})
	oldReady, releaseOld := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseOld) }) }
	var held atomic.Bool
	handler := s.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/api/register" && r.URL.Query().Get("id") == "old-task" && held.CompareAndSwap(false, true) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			close(oldReady)
			<-releaseOld
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer release()
	ctx := resourceRegressionBrowser(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(server.URL),
		chromedp.Click("#loginSubmit", chromedp.ByQuery),
		chromedp.WaitNotPresent("body.signed-out", chromedp.ByQuery),
		chromedp.Click(`nav button[data-tab="tasks"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldReady:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	s.Registers.put(&registerJob{ID: "current-task", StartedAt: time.Now().Unix() + 1, Count: 3, running: true, logs: []string{"CURRENT TASK LOG"}})
	if err := chromedp.Run(ctx,
		chromedp.Click(`nav button[data-tab="accounts"]`, chromedp.ByQuery),
		chromedp.Click(`nav button[data-tab="tasks"]`, chromedp.ByQuery),
		chromedp.WaitReady(`#registerHistory option[value="current-task"]`, chromedp.ByQuery),
		chromedp.SetValue("#registerHistory", "current-task", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#regLogs').textContent.includes('CURRENT TASK LOG')`, nil),
		chromedp.Poll(`performance.getEntriesByType('resource').some(e => e.name.endsWith('/admin/api/register?id=current-task'))`, nil),
	); err != nil {
		t.Fatal(err)
	}
	release()
	var current bool
	if err := chromedp.Run(ctx,
		chromedp.Poll(`performance.getEntriesByType('resource').some(e => e.name.endsWith('/admin/api/register?id=old-task') && e.responseEnd > 0)`, nil),
		// Allow response body promises and rendering callbacks to complete.
		chromedp.Evaluate(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
		chromedp.Evaluate(`document.querySelector('#regLogs').textContent.includes('CURRENT TASK LOG') && !document.querySelector('#regLogs').textContent.includes('OLD TASK LOG') && document.querySelector('#regStatus').textContent.includes('/3')`, &current),
	); err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Fatal("stale registration poll replaced the current task's details")
	}
}

func resourceRegressionBrowser(t *testing.T) context.Context {
	t.Helper()
	exe, err := googleauth.FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(exe), chromedp.Headless, chromedp.NoSandbox, chromedp.WindowSize(1440, 1100))
	allocator, stopAllocator := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(stopAllocator)
	browser, stopBrowser := chromedp.NewContext(allocator)
	t.Cleanup(stopBrowser)
	ctx, cancel := context.WithTimeout(browser, 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestResourcesConsoleLocksDrawerWhileSaving(t *testing.T) {
	s := newEmptyServer(t)
	s.Store.UpsertAccount(&deepai.Account{ID: "acc-pending", Name: "Before save", APIKey: "fixture-pending-key", Enabled: true})
	saveReady, releaseSave := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSave) }) }
	var held atomic.Bool
	handler := s.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/api/accounts" && r.Method == http.MethodPost && held.CompareAndSwap(false, true) {
			close(saveReady)
			<-releaseSave
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer release()
	ctx := resourceRegressionBrowser(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(server.URL),
		chromedp.Click("#loginSubmit", chromedp.ByQuery),
		chromedp.WaitNotPresent("body.signed-out", chromedp.ByQuery),
		chromedp.Click(`nav button[data-tab="accounts"]`, chromedp.ByQuery),
		chromedp.Click(`button[data-account-id="acc-pending"]`, chromedp.ByQuery),
		chromedp.SetValue("#editName", "Submitted edit", chromedp.ByQuery),
		chromedp.Click("#resourceSave", chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-saveReady:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var locked bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('#resourceDialog').open && document.querySelector('#resourceSave').disabled && document.querySelector('#editName').disabled && document.querySelector('#editGroup').disabled`, &locked)); err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("pending save allows editing fields whose new values would be lost on completion")
	}
	release()
	if err := chromedp.Run(ctx, chromedp.Poll(`!document.querySelector('#resourceDialog').open`, nil)); err != nil {
		t.Fatal(err)
	}
	if got := s.Store.AccountCopy("acc-pending"); got == nil || got.Name != "Submitted edit" {
		t.Fatal("pending account edit did not persist on completion")
	}
}

func TestResourcesConsoleKeyAndMailboxMaintenance(t *testing.T) {
	exe, err := googleauth.FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	s := newEmptyServer(t)
	s.Store.AddKey("Fixture key", "default")
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(exe), chromedp.Headless, chromedp.NoSandbox, chromedp.WindowSize(1440, 1100))
	allocator, stopAllocator := chromedp.NewExecAllocator(context.Background(), opts...)
	defer stopAllocator()
	browser, stopBrowser := chromedp.NewContext(allocator)
	defer stopBrowser()
	ctx, cancel := context.WithTimeout(browser, 90*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx); err != nil {
		t.Fatal(err)
	}
	run := func(stage string, actions ...chromedp.Action) {
		t.Helper()
		stageCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		if err := chromedp.Run(stageCtx, actions...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	clickText := func(text string) chromedp.Action {
		return chromedp.Click(`//*[@id="resourceDetails"]//button[normalize-space()="`+text+`"]`, chromedp.BySearch)
	}
	run("edit gateway key",
		chromedp.Navigate(server.URL),
		chromedp.Click("#loginSubmit", chromedp.ByQuery),
		chromedp.WaitNotPresent("body.signed-out", chromedp.ByQuery),
		chromedp.Click(`nav button[data-tab="keys"]`, chromedp.ByQuery),
		chromedp.Click("#keys .resource-name", chromedp.ByQuery),
		chromedp.WaitVisible("#resourceDialog[open]", chromedp.ByQuery),
		chromedp.SetValue("#editName", "Updated fixture key", chromedp.ByQuery),
		chromedp.SetValue("#editGroup", "production", chromedp.ByQuery),
		chromedp.Click("#resourceSave", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#resourceDialog').open && document.querySelector('#keys').textContent.includes('Updated fixture key')`, nil),
	)
	if keys := s.Store.Keys(); len(keys) != 1 || keys[0].Name != "Updated fixture key" || keys[0].Group != "production" {
		t.Fatal("key edit was not persisted")
	}
	run("cancel gateway key revocation",
		chromedp.Click("#keys tr td:last-child button", chromedp.ByQuery),
		clickText("撤销密钥"),
		chromedp.WaitVisible("#confirmDialog[open]", chromedp.ByQuery),
		chromedp.Click("#confirmCancel", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#confirmDialog').open`, nil),
	)
	if len(s.Store.Keys()) != 1 {
		t.Fatal("cancelled key revocation changed stored keys")
	}
	run("confirm gateway key revocation",
		clickText("撤销密钥"),
		chromedp.WaitVisible("#confirmDialog[open]", chromedp.ByQuery),
		chromedp.Click("#confirmAccept", chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('#resourceDialog').open && document.querySelectorAll('#keys .resource-name').length === 0`, nil),
	)
	if len(s.Store.Keys()) != 0 {
		t.Fatal("confirmed key revocation did not remove stored key")
	}
	for _, kind := range []string{"google", "mail"} {
		prefix, rows, fileSelector, importSelector, statusSelector := "google", "#googleAccounts", "#googleFile", "#googleImport", "#googleImportStatus"
		if kind == "mail" {
			prefix, rows, fileSelector, importSelector, statusSelector = "mail", "#graphAccounts", "#graphMailFile", "#graphMailImport", "#graphMailStatus"
		}
		run(kind+" add mailbox",
			chromedp.Click(`nav button[data-tab="register"]`, chromedp.ByQuery),
			chromedp.Click(`button[data-provider="`+kind+`"]`, chromedp.ByQuery),
			chromedp.Click("#"+prefix+"Add", chromedp.ByQuery),
			chromedp.WaitVisible("#resourceDialog[open]", chromedp.ByQuery),
			chromedp.SetValue("#editEmail", "first@example.invalid", chromedp.ByQuery),
			chromedp.SetValue("#editPassword", "fixture-initial-password", chromedp.ByQuery),
		)
		if kind == "mail" {
			run("Graph credentials",
				chromedp.SetValue("#editClientID", "fixture-client-id", chromedp.ByQuery),
				chromedp.SetValue("#editRefreshToken", "fixture-refresh-token", chromedp.ByQuery),
			)
		}
		run(kind+" save and reopen mailbox",
			chromedp.Click("#resourceSave", chromedp.ByQuery),
			chromedp.Poll(`!document.querySelector('#resourceDialog').open && document.querySelectorAll('`+rows+` .resource-name').length === 1`, nil),
			chromedp.Click(rows+" .resource-name", chromedp.ByQuery),
			clickText("更新凭据"),
			chromedp.WaitVisible("#editPassword", chromedp.ByQuery),
		)
		var freshFields bool
		run(kind+" credentials stay hidden", chromedp.Evaluate(`document.querySelector('#editPassword').value === '' && document.querySelector('#editEmail').readOnly`, &freshFields))
		if !freshFields {
			t.Fatalf("%s credential editor exposed saved password or allowed changing identity", kind)
		}
		run(kind+" update password", chromedp.SetValue("#editPassword", "fixture-replacement-password", chromedp.ByQuery))
		if kind == "mail" {
			run("Graph replacement credentials",
				chromedp.SetValue("#editClientID", "fixture-new-client", chromedp.ByQuery),
				chromedp.SetValue("#editRefreshToken", "fixture-new-token", chromedp.ByQuery),
			)
		}
		run(kind+" persist updated credentials",
			chromedp.Click("#resourceSave", chromedp.ByQuery),
			chromedp.Poll(`!document.querySelector('#resourceDialog').open`, nil),
		)
		if kind == "mail" {
			data, err := os.ReadFile(s.graphMailPath())
			if err != nil || !strings.Contains(string(data), "fixture-replacement-password----fixture-new-client----fixture-new-token") {
				t.Fatal("Graph credential update was not persisted")
			}
		}
		content := "first@example.invalid----fixture-import-password\nsecond@example.invalid----fixture-import-password\n"
		if kind == "mail" {
			content = strings.ReplaceAll(content, "fixture-import-password", "fixture-import-password----fixture-import-client----fixture-import-token")
		}
		file := filepath.Join(t.TempDir(), kind+"-import.txt")
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		run(kind+" preview import",
			chromedp.Evaluate(`document.querySelector('`+fileSelector+`').closest('details').open = true`, nil),
			chromedp.SetUploadFiles(fileSelector, []string{file}, chromedp.ByQuery),
			chromedp.Click(importSelector, chromedp.ByQuery),
			chromedp.WaitVisible("#confirmDialog[open]", chromedp.ByQuery),
		)
		var previewOK bool
		run(kind+" inspect preview", chromedp.Evaluate(`document.querySelector('#confirmMessage').textContent.includes('新增 1') && document.querySelector('#confirmMessage').textContent.includes('更新 1') && document.querySelectorAll('`+rows+` .resource-name').length === 1 && !document.body.innerText.includes('fixture-import-password')`, &previewOK))
		if !previewOK {
			t.Fatalf("%s preview counts, isolation or credential masking failed", kind)
		}
		if kind == "google" && len(s.Store.GoogleAccounts()) != 1 {
			t.Fatal("Google import preview changed the pool")
		}
		run(kind+" cancel import",
			chromedp.Click("#confirmCancel", chromedp.ByQuery),
			chromedp.Poll(`document.querySelector('`+statusSelector+`').textContent.includes('已取消') && !document.querySelector('`+importSelector+`').disabled`, nil),
		)
		run(kind+" confirm import",
			chromedp.Click(importSelector, chromedp.ByQuery),
			chromedp.WaitVisible("#confirmDialog[open]", chromedp.ByQuery),
			chromedp.Click("#confirmAccept", chromedp.ByQuery),
			chromedp.Poll(`document.querySelectorAll('`+rows+` .resource-name').length === 2 && !document.querySelector('`+importSelector+`').disabled`, nil),
		)
		run(kind+" remove local mailbox credentials",
			chromedp.Click("#"+prefix+"SelectPage", chromedp.ByQuery),
			chromedp.Click("#"+prefix+"BulkRemove", chromedp.ByQuery),
			chromedp.WaitVisible("#confirmDialog[open]", chromedp.ByQuery),
			chromedp.Click("#confirmAccept", chromedp.ByQuery),
			chromedp.Poll(`document.querySelectorAll('`+rows+` .resource-name').length === 0`, nil),
		)
		if kind == "google" && len(s.Store.GoogleAccounts()) != 0 {
			t.Fatal("Google credentials remained after confirmed deletion")
		}
		if kind == "mail" {
			data, err := os.ReadFile(s.graphMailPath())
			if (err != nil && !os.IsNotExist(err)) || strings.Contains(string(data), "@example.invalid") {
				t.Fatal("Graph credentials remained after confirmed deletion")
			}
		}
	}
}
