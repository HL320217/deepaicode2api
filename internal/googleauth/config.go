package googleauth

import (
	"errors"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// NewBrowserFromEnv keeps visible login on desktop machines and uses headless
// Chromium on Linux servers without an X11 or Wayland display.
func NewBrowserFromEnv() (*Browser, error) {
	headless, err := headlessMode(os.Getenv("DEEPAI_GOOGLE_HEADLESS"), runtime.GOOS, os.Getenv("DISPLAY"), os.Getenv("WAYLAND_DISPLAY"))
	if err != nil {
		return nil, err
	}
	b := &Browser{Headless: headless, Proxy: strings.TrimSpace(os.Getenv("DEEPAI_GOOGLE_PROXY"))}
	if err := validateProxy(b.Proxy); err != nil {
		return nil, err
	}
	return b, nil
}

func validateProxy(proxy string) error {
	if proxy == "" {
		return nil
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "socks5") {
		return errors.New("Google 浏览器代理需为不含账密的 http:// 或 socks5:// 地址")
	}
	return nil
}

func headlessMode(mode, goos, display, wayland string) (bool, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == "auto" {
		return goos == "linux" && strings.TrimSpace(display) == "" && strings.TrimSpace(wayland) == "", nil
	}
	value, err := strconv.ParseBool(mode)
	if err != nil {
		return false, errors.New("DEEPAI_GOOGLE_HEADLESS 必须为 auto、true 或 false")
	}
	return value, nil
}
