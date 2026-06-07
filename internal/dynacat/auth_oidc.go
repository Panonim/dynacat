package dynacat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const OIDC_STATE_COOKIE_NAME = "oidc_state"
const OIDC_PKCE_COOKIE_NAME = "oidc_pkce"
const OIDC_STATE_VALID_PERIOD = 5 * time.Minute

func initOIDCProvider(cfg *oidcConfig) (*gooidc.Provider, *gooidc.IDTokenVerifier, *oauth2.Config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	provider, err := gooidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("initializing OIDC provider from %s: %w", cfg.IssuerURL, err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}

	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       scopes,
	}

	verifier := provider.Verifier(&gooidc.Config{ClientID: cfg.ClientID})

	return provider, verifier, oauth2Cfg, nil
}

func (a *application) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	state, err := makeAuthSecretKey(16)
	if err != nil {
		log.Printf("OIDC: could not generate state: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	pkceVerifier := oauth2.GenerateVerifier()

	secure := a.isRequestHTTPS(r)

	http.SetCookie(w, &http.Cookie{
		Name:     OIDC_STATE_COOKIE_NAME,
		Value:    state,
		Expires:  time.Now().Add(OIDC_STATE_VALID_PERIOD),
		Secure:   secure,
		Path:     a.Config.Server.BaseURL + "/",
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     OIDC_PKCE_COOKIE_NAME,
		Value:    pkceVerifier,
		Expires:  time.Now().Add(OIDC_STATE_VALID_PERIOD),
		Secure:   secure,
		Path:     a.Config.Server.BaseURL + "/",
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
	})

	url := a.oauth2Config.AuthCodeURL(state, oauth2.S256ChallengeOption(pkceVerifier))
	http.Redirect(w, r, url, http.StatusFound)
}

func (a *application) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	baseURL := a.Config.Server.BaseURL

	// Validate state
	stateCookie, err := r.Cookie(OIDC_STATE_COOKIE_NAME)
	if err != nil || stateCookie.Value == "" {
		http.Redirect(w, r, baseURL+"/login?error=invalid_state", http.StatusSeeOther)
		return
	}

	if r.URL.Query().Get("state") != stateCookie.Value {
		http.Redirect(w, r, baseURL+"/login?error=invalid_state", http.StatusSeeOther)
		return
	}

	pkceCookie, err := r.Cookie(OIDC_PKCE_COOKIE_NAME)
	if err != nil || pkceCookie.Value == "" {
		http.Redirect(w, r, baseURL+"/login?error=invalid_state", http.StatusSeeOther)
		return
	}

	// Clear state + PKCE cookies
	http.SetCookie(w, &http.Cookie{
		Name:     OIDC_STATE_COOKIE_NAME,
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		Path:     baseURL + "/",
		HttpOnly: true,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     OIDC_PKCE_COOKIE_NAME,
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		Path:     baseURL + "/",
		HttpOnly: true,
	})

	// Exchange code for token
	code := r.URL.Query().Get("code")
	if code == "" {
		errParam := r.URL.Query().Get("error")
		if errParam == "" {
			errParam = "missing_code"
		}
		http.Redirect(w, r, baseURL+"/login?error="+errParam, http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	oauth2Token, err := a.oauth2Config.Exchange(ctx, code, oauth2.VerifierOption(pkceCookie.Value))
	if err != nil {
		log.Printf("OIDC: token exchange failed: %v", err)
		http.Redirect(w, r, baseURL+"/login?error=token_exchange", http.StatusSeeOther)
		return
	}

	// Extract and verify ID token
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Redirect(w, r, baseURL+"/login?error=no_id_token", http.StatusSeeOther)
		return
	}

	idToken, err := a.oidcVerifier.Verify(ctx, rawIDToken)
	if err != nil {
		log.Printf("OIDC: ID token verification failed: %v", err)
		http.Redirect(w, r, baseURL+"/login?error=token_verify", http.StatusSeeOther)
		return
	}

	// Extract claims
	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		log.Printf("OIDC: could not extract claims: %v", err)
		http.Redirect(w, r, baseURL+"/login?error=claims", http.StatusSeeOther)
		return
	}

	usernameClaim := a.Config.Auth.OIDC.UsernameClaim
	if usernameClaim == "" {
		usernameClaim = "preferred_username"
	}

	username, _ := claims[usernameClaim].(string)
	if username == "" {
		// Fallback to sub
		username, _ = claims["sub"].(string)
	}
	if username == "" {
		http.Redirect(w, r, baseURL+"/login?error=no_username", http.StatusSeeOther)
		return
	}

	groupsClaim := a.Config.Auth.OIDC.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = "groups"
	}
	groups := extractGroupsClaim(claims, groupsClaim)

	// Check OIDC-level allowed users/groups restrictions
	oidcCfg := a.Config.Auth.OIDC
	if len(oidcCfg.AllowedUsers) > 0 || len(oidcCfg.AllowedGroups) > 0 {
		allowed := false
		for _, u := range oidcCfg.AllowedUsers {
			if u == username {
				allowed = true
				break
			}
		}
		if !allowed {
		outer:
			for _, g := range oidcCfg.AllowedGroups {
				for _, ug := range groups {
					if g == ug {
						allowed = true
						break outer
					}
				}
			}
		}
		if !allowed {
			log.Printf("OIDC: user %s not in allowed users/groups", username)
			http.Redirect(w, r, baseURL+"/login?error=not_allowed", http.StatusSeeOther)
			return
		}
	}

	// Generate session ID and store session
	sessionID, err := makeAuthSecretKey(32)
	if err != nil {
		log.Printf("OIDC: could not generate session ID: %v", err)
		http.Redirect(w, r, baseURL+"/login?error=internal", http.StatusSeeOther)
		return
	}

	a.oidcSessions.set(sessionID, &oidcSession{
		Username:  username,
		Groups:    groups,
		Token:     oauth2Token,
		CreatedAt: time.Now(),
	})

	http.SetCookie(w, &http.Cookie{
		Name:     OIDC_SESSION_COOKIE_NAME,
		Value:    sessionID,
		Expires:  time.Now().Add(OIDC_SESSION_VALID_PERIOD),
		Secure:   a.isRequestHTTPS(r),
		Path:     baseURL + "/",
		SameSite: http.SameSiteStrictMode,
		HttpOnly: true,
	})

	log.Printf("OIDC: user %s logged in", username)
	http.Redirect(w, r, baseURL+"/", http.StatusSeeOther)
}

func extractGroupsClaim(claims map[string]interface{}, claimName string) []string {
	raw, ok := claims[claimName]
	if !ok {
		return nil
	}

	switch v := raw.(type) {
	case []interface{}:
		groups := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				groups = append(groups, s)
			}
		}
		return groups
	case string:
		if v == "" {
			return nil
		}
		// Try comma-separated first, then space-separated
		if strings.Contains(v, ",") {
			parts := strings.Split(v, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			return parts
		}
		return strings.Fields(v)
	case json.RawMessage:
		var items []string
		if err := json.Unmarshal(v, &items); err == nil {
			return items
		}
	}

	return nil
}

