package server

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"deepaicode2api/internal/deepai"
	"deepaicode2api/internal/googleauth"
)

type googleLoginIdleTimeoutError struct {
	timeout time.Duration
}

func (e *googleLoginIdleTimeoutError) Error() string {
	return fmt.Sprintf("连续 %g 秒没有新的授权进展，已自动跳过；可勾选重试失败账号重新授权", e.timeout.Seconds())
}

func googleLoginIdleTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("DEEPAI_GOOGLE_IDLE_TIMEOUT"))
	if raw == "" {
		raw = "60s"
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout <= 0 || timeout > loginTTL {
		return 0, fmt.Errorf("DEEPAI_GOOGLE_IDLE_TIMEOUT 必须是大于 0 且不超过 10m 的时长，例如 60s")
	}
	return timeout, nil
}

// runGoogleLogin limits waiting for manual intervention, repeated page errors,
// stalled navigation and missing callbacks without shortening a progressing
// account's overall deadline. Repeated notices do not renew the idle deadline.
func (s *Server) runGoogleLogin(ctx context.Context, timeout time.Duration, credential googleauth.Credential, report func(string, string)) (*deepai.Credentials, error) {
	login := s.googleLogin
	if login == nil {
		login = s.loginGoogle
	}
	loginCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var mu sync.Mutex
	deadline := time.Now().Add(timeout)
	seen := map[string]bool{}
	stopped := false
	var timer *time.Timer
	mu.Lock()
	timer = time.AfterFunc(timeout, func() {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return
		}
		// A progress report may have extended the deadline while this callback
		// was queued. Recheck it under the same lock before cancelling Chrome.
		if remaining := time.Until(deadline); remaining > 0 {
			timer.Reset(remaining)
			return
		}
		stopped = true
		cancel(&googleLoginIdleTimeoutError{timeout: timeout})
	})
	mu.Unlock()
	defer func() {
		mu.Lock()
		stopped = true
		timer.Stop()
		mu.Unlock()
	}()
	creds, err := login(loginCtx, credential, func(phase, message string) {
		switch phase {
		case "browser", "google", "email", "password", "onboarding", "consent":
			mu.Lock()
			key := phase + "\x00" + message
			if !stopped && !seen[key] {
				seen[key] = true
				deadline = time.Now().Add(timeout)
			}
			mu.Unlock()
		}
		report(phase, message)
	})
	mu.Lock()
	stopped = true
	timer.Stop()
	cause := context.Cause(loginCtx)
	mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if cause != nil {
		return nil, cause
	}
	return creds, err
}
