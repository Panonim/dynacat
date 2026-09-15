package dynacat

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsSafeLocalPath(t *testing.T) {
	cases := map[string]bool{
		"/":                    true,
		"/?code=123":           true,
		"/page?x=y&z=1":        true,
		"/dynacat/?code=abc":   true,
		"":                     false,
		"//evil.com":           false,
		"/\\evil.com":          false,
		"https://evil.com":     false,
		"http://evil.com/path": false,
		"javascript:alert(1)":  false,
		"evil.com":             false,
	}
	for in, want := range cases {
		if got := isSafeLocalPath(in); got != want {
			t.Errorf("isSafeLocalPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginRedirectRoundTrip(t *testing.T) {
	app := &application{} // BaseURL = "", HTTPS = false

	// Bouncing an unauthenticated request remembers the original path+query.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/secret?code=123", nil)
	app.redirectToLoginPage(rec, req)

	if loc := rec.Result().Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
	saved := findCookie(rec.Result().Cookies(), AUTH_REDIRECT_COOKIE_NAME)
	if saved == nil || saved.Value != "/secret?code=123" {
		t.Fatalf("redirect cookie = %v, want value /secret?code=123", saved)
	}

	// On a later request the destination is returned and the cookie is cleared.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/oidc/callback", nil)
	req2.AddCookie(saved)
	if got := app.takeLoginRedirect(rec2, req2); got != "/secret?code=123" {
		t.Errorf("takeLoginRedirect = %q, want /secret?code=123", got)
	}
	if cleared := findCookie(rec2.Result().Cookies(), AUTH_REDIRECT_COOKIE_NAME); cleared == nil || cleared.Value != "" {
		t.Errorf("expected redirect cookie to be cleared, got %v", cleared)
	}

	// A tampered, off-origin cookie value is ignored in favor of the safe default.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/api/oidc/callback", nil)
	req3.AddCookie(&http.Cookie{Name: AUTH_REDIRECT_COOKIE_NAME, Value: "//evil.com"})
	if got := app.takeLoginRedirect(rec3, req3); got != "/" {
		t.Errorf("takeLoginRedirect with off-origin value = %q, want /", got)
	}
}

func TestLoginDestination(t *testing.T) {
	cases := []struct {
		name            string
		oidcEnabled     bool
		passwordEnabled bool
		want            string
	}{
		{"password only", false, true, "/login"},
		{"password + oidc", true, true, "/login"},
		{"oidc only", true, false, "/api/oidc/login"},
		{"no auth", false, false, "/login"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &application{OIDCEnabled: tc.oidcEnabled, PasswordEnabled: tc.passwordEnabled}
			if got := app.loginDestination(); got != tc.want {
				t.Errorf("loginDestination() = %q, want %q", got, tc.want)
			}
			if got := app.oidcOnlyAuth(); got != tc.oidcEnabled && tc.passwordEnabled == false && tc.oidcEnabled {
				t.Errorf("oidcOnlyAuth() = %v, want true", got)
			}
		})
	}
}

func TestRedirectToLoginPageDestination(t *testing.T) {
	cases := []struct {
		name            string
		oidcEnabled     bool
		passwordEnabled bool
		want            string
	}{
		{"password + oidc goes to login page", true, true, "/login"},
		{"oidc only skips interstitial", true, false, "/api/oidc/login"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &application{OIDCEnabled: tc.oidcEnabled, PasswordEnabled: tc.passwordEnabled}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/secret?x=1", nil)
			app.redirectToLoginPage(rec, req)
			if loc := rec.Result().Header.Get("Location"); loc != tc.want {
				t.Errorf("Location = %q, want %q", loc, tc.want)
			}
			// The redirect cookie must still be set so the user returns to the
			// original page after the OIDC flow completes.
			saved := findCookie(rec.Result().Cookies(), AUTH_REDIRECT_COOKIE_NAME)
			if saved == nil || saved.Value != "/secret?x=1" {
				t.Errorf("redirect cookie = %v, want value /secret?x=1", saved)
			}
		})
	}
}

func TestHandleUnauthorizedResponseDestination(t *testing.T) {
	app := &application{RequiresAuth: true, OIDCEnabled: true, PasswordEnabled: false}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if !app.handleUnauthorizedResponse(rec, req, redirectToLogin) {
		t.Fatal("expected handleUnauthorizedResponse to report unauthorized")
	}
	if loc := rec.Result().Header.Get("Location"); loc != "/api/oidc/login" {
		t.Errorf("Location = %q, want /api/oidc/login", loc)
	}
}
