package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"deepaicode2api/internal/store"
)

func TestAdminSecretRevealRequiresAdminAndExplicitRequest(t *testing.T) {
	s, gatewayKey := newTestServer(t, nil)
	s.Store.UpdateSettings(func(v *store.Settings) { v.AdminPassword = "fixture-admin" })
	keyID := s.Store.Keys()[0].PublicID()
	request := func(method, body, password, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/admin/api/secrets", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		if password != "" {
			req.Header.Set("X-Admin-Password", password)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	for _, password := range []string{"", gatewayKey} {
		if r := request("POST", `{"kind":"account","id":"acc-1"}`, password, ""); r.Code != 401 {
			t.Fatalf("non-admin reveal: %d", r.Code)
		}
	}
	if r := request("POST", `{"kind":"account","id":"acc-1"}`, "fixture-admin", "https://outside.invalid"); r.Code != 403 {
		t.Fatalf("cross-origin reveal: %d", r.Code)
	}
	for _, tc := range []struct{ kind, id, want string }{{"account", "acc-1", "upstream-key"}, {"key", keyID, gatewayKey}} {
		r := request("POST", mustMarshal(t, map[string]string{"kind": tc.kind, "id": tc.id}), "fixture-admin", "")
		if r.Code != 200 {
			t.Fatalf("reveal %s: %d", tc.kind, r.Code)
		}
		result := decodeJSON[map[string]string](t, r.Body.Bytes())
		if result["key"] != tc.want {
			t.Fatalf("incorrect secret for %s", tc.kind)
		}
		if r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("secret response must not be cached")
		}
	}
	for _, tc := range []struct {
		method, body string
		code         int
	}{{"GET", "", 405}, {"POST", `{"kind":"account","id":"missing"}`, 404}, {"POST", `{"kind":"key","id":"missing"}`, 404}, {"POST", `{"kind":"google","id":"acc-1"}`, 400}, {"POST", `{"kind":"key"}`, 400}} {
		if r := request(tc.method, tc.body, "fixture-admin", ""); r.Code != tc.code {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.body, r.Code, tc.code)
		}
	}
	for _, path := range []string{"/admin/api/state", "/admin/api/keys"} {
		r := call(t, s, "127.0.0.1:1234", map[string]string{"X-Admin-Password": "fixture-admin"}, path)
		if strings.Contains(r.Body.String(), gatewayKey) || strings.Contains(r.Body.String(), `"upstream-key"`) {
			t.Fatal("list must remain masked")
		}
	}
}
