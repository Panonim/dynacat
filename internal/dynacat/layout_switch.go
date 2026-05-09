package dynacat

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const (
	layoutCookieName     = "dynacat-layout"
	layoutProfilePrimary = "primary"
	layoutProfileNarrow  = "narrow"
)

// layoutDispatcher routes each request to either the primary or narrow application
// handler based on the dynacat-layout cookie value.
type layoutDispatcher struct {
	mu      sync.RWMutex
	primary http.Handler
	narrow  http.Handler
}

func (d *layoutDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	primary := d.primary
	narrow := d.narrow
	d.mu.RUnlock()

	if narrow != nil {
		if c, err := r.Cookie(layoutCookieName); err == nil && c.Value == layoutProfileNarrow {
			narrow.ServeHTTP(w, r)
			return
		}
	}
	primary.ServeHTTP(w, r)
}

func (d *layoutDispatcher) update(primary, narrow http.Handler) {
	d.mu.Lock()
	d.primary = primary
	d.narrow = narrow
	d.mu.Unlock()
}

// handleSetLayoutProfileRequest handles POST /api/set-layout-profile/{profile}.
// It sets the dynacat-layout cookie so the dispatcher routes subsequent page loads to
// the correct application. Auth matches handleThemeChangeRequest when auth is enabled.
func (a *application) handleSetLayoutProfileRequest(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}

	profile := r.PathValue("profile")
	if profile != layoutProfilePrimary && profile != layoutProfileNarrow {
		http.Error(w, "invalid profile", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     layoutCookieName,
		Value:    profile,
		Path:     a.Config.Server.BaseURL + "/",
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(2 * 365 * 24 * time.Hour),
	})
	w.WriteHeader(http.StatusNoContent)
}

func sortedStringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := slices.Clone(a)
	bb := slices.Clone(b)
	slices.Sort(aa)
	slices.Sort(bb)
	return slices.Equal(aa, bb)
}

func requireAuthPointersEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func validateLayoutPairOIDC(primaryOIDC, narrowOIDC *oidcConfig) error {
	pNil := primaryOIDC == nil
	nNil := narrowOIDC == nil
	if pNil && nNil {
		return nil
	}
	if pNil != nNil {
		return fmt.Errorf("narrow-viewport-config: auth.oidc must be set in both configs or omitted in both (dual-layout requires identical OIDC settings)")
	}
	p, n := primaryOIDC, narrowOIDC
	if p.IssuerURL != n.IssuerURL {
		return fmt.Errorf("narrow-viewport-config: auth.oidc.issuer-url must match the primary config")
	}
	if p.ClientID != n.ClientID {
		return fmt.Errorf("narrow-viewport-config: auth.oidc.client-id must match the primary config")
	}
	if p.ClientSecret != n.ClientSecret {
		return fmt.Errorf("narrow-viewport-config: auth.oidc.client-secret must match the primary config")
	}
	if p.RedirectURL != n.RedirectURL {
		return fmt.Errorf("narrow-viewport-config: auth.oidc.redirect-url must match the primary config")
	}
	if !sortedStringsEqual(p.Scopes, n.Scopes) {
		return fmt.Errorf("narrow-viewport-config: auth.oidc.scopes must match the primary config (same entries, order may differ)")
	}
	if p.UsernameClaim != n.UsernameClaim {
		return fmt.Errorf("narrow-viewport-config: auth.oidc.username-claim must match the primary config")
	}
	if p.GroupsClaim != n.GroupsClaim {
		return fmt.Errorf("narrow-viewport-config: auth.oidc.groups-claim must match the primary config")
	}
	if !sortedStringsEqual(p.AllowedGroups, n.AllowedGroups) {
		return fmt.Errorf("narrow-viewport-config: auth.oidc.allowed-groups must match the primary config")
	}
	if !sortedStringsEqual(p.AllowedUsers, n.AllowedUsers) {
		return fmt.Errorf("narrow-viewport-config: auth.oidc.allowed-users must match the primary config")
	}
	return nil
}

func validateLayoutPairAuth(primary, narrow *config) error {
	if primary.Auth.DisablePassword != narrow.Auth.DisablePassword {
		return fmt.Errorf("narrow-viewport-config: auth.disable-password must match the primary config")
	}
	if !requireAuthPointersEqual(primary.Auth.RequireAuth, narrow.Auth.RequireAuth) {
		return fmt.Errorf("narrow-viewport-config: auth.require-auth must match the primary config")
	}
	if err := validateLayoutPairOIDC(primary.Auth.OIDC, narrow.Auth.OIDC); err != nil {
		return err
	}
	if len(primary.Auth.Users) != len(narrow.Auth.Users) {
		return fmt.Errorf("narrow-viewport-config: auth.users must define the same usernames as the primary config")
	}
	for username := range primary.Auth.Users {
		if _, ok := narrow.Auth.Users[username]; !ok {
			return fmt.Errorf("narrow-viewport-config: auth.users is missing username %q (present in primary config)", username)
		}
	}
	return nil
}

// validateLayoutPairConfigs ensures that server-critical settings are identical
// between the primary and narrow configs. Pages, widgets, theme, and branding may
// differ freely between the two files.
func validateLayoutPairConfigs(primary, narrow *config) error {
	type check struct {
		name string
		a, b any
	}
	checks := []check{
		{"server.host", primary.Server.Host, narrow.Server.Host},
		{"server.port", primary.Server.Port, narrow.Server.Port},
		{"server.base-url", primary.Server.BaseURL, narrow.Server.BaseURL},
		{"server.https", primary.Server.HTTPS, narrow.Server.HTTPS},
		{"server.proxied", primary.Server.Proxied, narrow.Server.Proxied},
		{"server.assets-path", primary.Server.AssetsPath, narrow.Server.AssetsPath},
		{"server.cache-dir", primary.Server.CacheDir, narrow.Server.CacheDir},
		{"server.db-path", primary.Server.DBPath, narrow.Server.DBPath},
		{"auth.secret-key", primary.Auth.SecretKey, narrow.Auth.SecretKey},
	}
	for _, c := range checks {
		if fmt.Sprintf("%v", c.a) != fmt.Sprintf("%v", c.b) {
			return fmt.Errorf("narrow-viewport-config: %s must match the primary config (%v vs %v)", c.name, c.a, c.b)
		}
	}
	if !sortedStringsEqual(primary.Server.TrustedProxies, narrow.Server.TrustedProxies) {
		return fmt.Errorf("narrow-viewport-config: server.trusted-proxies must match the primary config (same entries, order may differ)")
	}
	if !sortedStringsEqual(primary.Server.AllowedEmbedHosts, narrow.Server.AllowedEmbedHosts) {
		return fmt.Errorf("narrow-viewport-config: server.allowed-embed-hosts must match the primary config (same entries, order may differ; CSP/frame rules use primary only)")
	}
	return validateLayoutPairAuth(primary, narrow)
}

// buildDualServer creates a single http.Server that dispatches between primaryApp and
// narrowApp based on the dynacat-layout cookie. The layout profile switching endpoint
// is registered on an outer mux so it is always reachable regardless of active profile.
func buildDualServer(primaryApp, narrowApp *application) (func() error, func() error) {
	outerMux := http.NewServeMux()
	outerMux.HandleFunc(
		"POST /api/set-layout-profile/{profile}",
		primaryApp.handleSetLayoutProfileRequest,
	)

	dispatcher := &layoutDispatcher{}
	dispatcher.update(primaryApp.handler(), narrowApp.handler())
	outerMux.Handle("/", dispatcher)

	assetsPath := primaryApp.Config.Server.AssetsPath
	if assetsPath == "" {
		assetsPath = "/app/assets"
	}
	absAssetsPath, _ := filepath.Abs(assetsPath)

	server := http.Server{
		Addr:    fmt.Sprintf("%s:%d", primaryApp.Config.Server.Host, primaryApp.Config.Server.Port),
		Handler: primaryApp.securityHeadersMiddleware(outerMux),
	}

	cancelPrimary := primaryApp.startAuxiliaryLoops()
	cancelNarrow := narrowApp.startAuxiliaryLoops()

	start := func() error {
		log.Printf(
			"Starting dual-layout server on %s:%d (base-url: %q, assets-path: %q)\n",
			primaryApp.Config.Server.Host, primaryApp.Config.Server.Port,
			primaryApp.Config.Server.BaseURL, absAssetsPath,
		)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}

	stop := func() error {
		cancelPrimary()
		cancelNarrow()
		return server.Close()
	}

	return start, stop
}
