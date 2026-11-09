package googleauth

import "testing"

func TestHeadlessModeSelection(t *testing.T) {
	for _, tc := range []struct {
		name, mode, goos, display, wayland string
		want, wantErr                      bool
	}{
		{name: "server default", goos: "linux", want: true},
		{name: "server auto", mode: "auto", goos: "linux", want: true},
		{name: "X11 desktop", goos: "linux", display: ":0"},
		{name: "Wayland desktop", goos: "linux", wayland: "wayland-0"},
		{name: "Windows default", goos: "windows"},
		{name: "explicit headless", mode: "true", goos: "windows", want: true},
		{name: "explicit visible", mode: "false", goos: "linux"},
		{name: "numeric headless", mode: "1", goos: "linux", want: true},
		{name: "invalid flag", mode: "sometimes", goos: "linux", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := headlessMode(tc.mode, tc.goos, tc.display, tc.wayland)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("headless=%v, err=%v", got, err)
			}
		})
	}
}

func TestBrowserEnvironmentConfiguration(t *testing.T) {
	t.Setenv("DEEPAI_GOOGLE_HEADLESS", "true")
	t.Setenv("DEEPAI_GOOGLE_PROXY", "http://127.0.0.1:9876")
	b, err := NewBrowserFromEnv()
	if err != nil || !b.Headless || b.Proxy != "http://127.0.0.1:9876" {
		t.Fatalf("browser config was not applied: %v", err)
	}
	t.Setenv("DEEPAI_GOOGLE_HEADLESS", "invalid")
	if _, err := NewBrowserFromEnv(); err == nil {
		t.Fatal("invalid headless config silently accepted")
	}
}

func TestBrowserRejectsInvalidProxyConfiguration(t *testing.T) {
	t.Setenv("DEEPAI_GOOGLE_HEADLESS", "true")
	for _, proxy := range []string{"https://example.test:443", "http://user:private-secret@example.test:8080", "not-a-proxy"} {
		t.Setenv("DEEPAI_GOOGLE_PROXY", proxy)
		if _, err := NewBrowserFromEnv(); err == nil {
			t.Errorf("invalid browser proxy accepted: %s", proxy)
		}
	}
}
