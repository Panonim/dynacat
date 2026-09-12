package dynacat

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const AUTH_SESSION_COOKIE_NAME = "session_token"
const AUTH_RATE_LIMIT_WINDOW = 5 * time.Minute
const AUTH_RATE_LIMIT_MAX_ATTEMPTS = 5

const AUTH_TOKEN_SECRET_LENGTH = 32
const AUTH_USERNAME_HASH_LENGTH = 32
const AUTH_SECRET_KEY_LENGTH = AUTH_TOKEN_SECRET_LENGTH + AUTH_USERNAME_HASH_LENGTH
const AUTH_TIMESTAMP_LENGTH = 8
const AUTH_TOKEN_DATA_LENGTH = AUTH_USERNAME_HASH_LENGTH + AUTH_TIMESTAMP_LENGTH

const AUTH_TOKEN_VALID_PERIOD = 14 * 24 * time.Hour
const AUTH_TOKEN_REGEN_BEFORE = 7 * 24 * time.Hour

var loginPageTemplate = mustParseTemplate("login.html", "document.html", "footer.html")

type authenticatedUser struct {
	Username string
	Groups   []string
	IsOIDC   bool
}

type doWhenUnauthorized int

const (
	redirectToLogin doWhenUnauthorized = iota
	showUnauthorizedJSON
)

type failedAuthAttempt struct {
	attempts int
	first    time.Time
}

func generateSessionToken(usernameHash []byte, secret []byte, now time.Time) (string, error) {
	if len(secret) != AUTH_SECRET_KEY_LENGTH {
		return "", fmt.Errorf("secret key length is not %d bytes", AUTH_SECRET_KEY_LENGTH)
	}

	if len(usernameHash) != AUTH_USERNAME_HASH_LENGTH {
		return "", fmt.Errorf("username hash length is not %d bytes", AUTH_USERNAME_HASH_LENGTH)
	}

	data := make([]byte, AUTH_TOKEN_DATA_LENGTH)
	copy(data, usernameHash)
	expires := now.Add(AUTH_TOKEN_VALID_PERIOD).Unix()
	binary.LittleEndian.PutUint64(data[AUTH_USERNAME_HASH_LENGTH:], uint64(expires))

	h := hmac.New(sha256.New, secret[0:AUTH_TOKEN_SECRET_LENGTH])
	h.Write(data)

	signature := h.Sum(nil)
	encodedToken := base64.StdEncoding.EncodeToString(append(data, signature...))

	return encodedToken, nil
}

// Mixing in the credential invalidates old tokens whenever the password changes.
func computeUsernameHash(username string, credential string, secret []byte) ([]byte, error) {
	if len(secret) != AUTH_SECRET_KEY_LENGTH {
		return nil, fmt.Errorf("secret key length is not %d bytes", AUTH_SECRET_KEY_LENGTH)
	}

	h := hmac.New(sha256.New, secret[AUTH_TOKEN_SECRET_LENGTH:])
	h.Write([]byte(username))
	h.Write([]byte{0})
	h.Write([]byte(credential))

	return h.Sum(nil), nil
}

func verifySessionToken(token string, secretBytes []byte, now time.Time) ([]byte, bool, error) {
	tokenBytes, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, false, err
	}

	if len(tokenBytes) != AUTH_TOKEN_DATA_LENGTH+32 {
		return nil, false, fmt.Errorf("token length is invalid")
	}

	if len(secretBytes) != AUTH_SECRET_KEY_LENGTH {
		return nil, false, fmt.Errorf("secret key length is not %d bytes", AUTH_SECRET_KEY_LENGTH)
	}

	usernameHashBytes := tokenBytes[0:AUTH_USERNAME_HASH_LENGTH]
	timestampBytes := tokenBytes[AUTH_USERNAME_HASH_LENGTH : AUTH_USERNAME_HASH_LENGTH+AUTH_TIMESTAMP_LENGTH]
	providedSignatureBytes := tokenBytes[AUTH_TOKEN_DATA_LENGTH:]

	h := hmac.New(sha256.New, secretBytes[0:32])
	h.Write(tokenBytes[0:AUTH_TOKEN_DATA_LENGTH])
	expectedSignatureBytes := h.Sum(nil)

	if !hmac.Equal(expectedSignatureBytes, providedSignatureBytes) {
		return nil, false, fmt.Errorf("signature does not match")
	}

	expiresTimestamp := int64(binary.LittleEndian.Uint64(timestampBytes))
	if now.Unix() > expiresTimestamp {
		return nil, false, fmt.Errorf("token has expired")
	}

	return usernameHashBytes,
		time.Unix(expiresTimestamp, 0).Add(-AUTH_TOKEN_REGEN_BEFORE).Before(now),
		nil
}

func makeAuthSecretKey(length int) (string, error) {
	key := make([]byte, length)
	_, err := rand.Read(key)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func (a *application) handleAuthenticationAttempt(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	waitOnFailure := 1*time.Second - time.Duration(mathrand.IntN(500))*time.Millisecond

	ip := a.addressOfRequest(r)

	if exceededRateLimit, retryAfter := a.checkAuthRateLimit(ip); exceededRateLimit {
		time.Sleep(waitOnFailure)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	err = json.Unmarshal(body, &creds)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	logAuthFailure := func() {
		slog.Warn("Failed login attempt", "username", strconv.Quote(creds.Username), "ip", ip)
	}

	if len(creds.Username) == 0 || len(creds.Password) == 0 {
		time.Sleep(waitOnFailure)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if len(creds.Username) > 50 || len(creds.Password) > 100 {
		logAuthFailure()
		time.Sleep(waitOnFailure)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	u, exists := a.Config.Auth.Users[creds.Username]
	if !exists {
		logAuthFailure()
		time.Sleep(waitOnFailure)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if err := bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(creds.Password)); err != nil {
		logAuthFailure()
		time.Sleep(waitOnFailure)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	token, err := generateSessionToken(u.usernameHash, a.authSecretKey, time.Now())
	if err != nil {
		slog.Error("Could not compute session token during login attempt", "error", err)
		time.Sleep(waitOnFailure)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	a.setAuthSessionCookie(w, r, token, time.Now().Add(AUTH_TOKEN_VALID_PERIOD))

	a.clearAuthRateLimit(ip)

	redirect := a.takeLoginRedirect(w, r)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"redirect": redirect})
}

// Counts a login attempt for the IP and reports whether it has run out of allowance.
// Also prunes attempts that have aged out of the window.
func (a *application) checkAuthRateLimit(ip string) (bool, int) {
	a.authAttemptsMu.Lock()
	defer a.authAttemptsMu.Unlock()

	attempt, exists := a.failedAuthAttempts[ip]
	if !exists {
		a.failedAuthAttempts[ip] = &failedAuthAttempt{attempts: 1, first: time.Now()}
		return false, 0
	}

	elapsed := time.Since(attempt.first)
	if elapsed < AUTH_RATE_LIMIT_WINDOW && attempt.attempts >= AUTH_RATE_LIMIT_MAX_ATTEMPTS {
		return true, max(1, int(AUTH_RATE_LIMIT_WINDOW.Seconds()-elapsed.Seconds()))
	}

	attempt.attempts++

	for ipOfAttempt := range a.failedAuthAttempts {
		if time.Since(a.failedAuthAttempts[ipOfAttempt].first) > AUTH_RATE_LIMIT_WINDOW {
			delete(a.failedAuthAttempts, ipOfAttempt)
		}
	}

	return false, 0
}

// Drops the IP's counter after a successful login so valid callers are never throttled.
func (a *application) clearAuthRateLimit(ip string) {
	a.authAttemptsMu.Lock()
	delete(a.failedAuthAttempts, ip)
	a.authAttemptsMu.Unlock()
}

// Verifies a username/password pair against the configured users. Password users have no
// groups, those only ever come from OIDC claims.
func (a *application) verifyUserPassword(username, password string) *authenticatedUser {
	if !a.PasswordEnabled || username == "" || password == "" {
		return nil
	}

	if len(username) > 50 || len(password) > 100 {
		return nil
	}

	u, exists := a.Config.Auth.Users[username]
	if !exists {
		return nil
	}

	if bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(password)) != nil {
		return nil
	}

	return &authenticatedUser{Username: username}
}

func (a *application) getAuthenticatedUser(w http.ResponseWriter, r *http.Request) *authenticatedUser {
	if a.PasswordEnabled && len(a.Config.Auth.Users) > 0 {
		token, err := r.Cookie(AUTH_SESSION_COOKIE_NAME)
		if err == nil && token.Value != "" {
			usernameHash, shouldRegenerate, err := verifySessionToken(token.Value, a.authSecretKey, time.Now())
			if err == nil {
				username, exists := a.usernameHashToUsername[string(usernameHash)]
				if exists {
					if u, exists := a.Config.Auth.Users[username]; exists {
						if shouldRegenerate {
							newToken, err := generateSessionToken(u.usernameHash, a.authSecretKey, time.Now())
							if err != nil {
								slog.Error("Could not compute session token during regeneration", "error", err)
							} else {
								a.setAuthSessionCookie(w, r, newToken, time.Now().Add(AUTH_TOKEN_VALID_PERIOD))
							}
						}
						return &authenticatedUser{Username: username, IsOIDC: false}
					}
				}
			}
		}
	}

	if a.OIDCEnabled && a.oidcSessions != nil {
		sessionCookie, err := r.Cookie(OIDC_SESSION_COOKIE_NAME)
		if err == nil && sessionCookie.Value != "" {
			sess, ok := a.oidcSessions.get(sessionCookie.Value)
			if ok {
				if time.Since(sess.CreatedAt) < OIDC_SESSION_VALID_PERIOD {
					return &authenticatedUser{
						Username: sess.Username,
						Groups:   sess.Groups,
						IsOIDC:   true,
					}
				}
				a.oidcSessions.delete(sessionCookie.Value)
			}
		}
	}

	return nil
}

func (a *application) isAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if !a.RequiresAuth {
		return true
	}
	return a.getAuthenticatedUser(w, r) != nil
}

func (a *application) isUserAllowedOnPage(user *authenticatedUser, p *page) bool {
	if len(p.AllowedUsers) == 0 && len(p.AllowedGroups) == 0 {
		return true
	}

	for _, u := range p.AllowedUsers {
		if u == user.Username {
			return true
		}
	}

	for _, g := range p.AllowedGroups {
		for _, ug := range user.Groups {
			if g == ug {
				return true
			}
		}
	}

	return false
}

func (a *application) canUserAccessPage(user *authenticatedUser, p *page) bool {
	pageHasRestrictions := len(p.AllowedUsers) > 0 || len(p.AllowedGroups) > 0

	if !pageHasRestrictions {
		if !a.RequiresAuth {
			return true
		}

		return user != nil
	}

	if user == nil {
		return false
	}

	return a.isUserAllowedOnPage(user, p)
}

func (a *application) handleAccessControl(w http.ResponseWriter, r *http.Request, p *page, fallback doWhenUnauthorized) bool {
	user := a.getAuthenticatedUser(w, r)

	if a.canUserAccessPage(user, p) {
		return false
	}

	if user != nil {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("Forbidden"))
		return true
	}

	switch fallback {
	case redirectToLogin:
		a.redirectToLoginPage(w, r)
	case showUnauthorizedJSON:
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Unauthorized"}`))
	}

	return true
}

func (a *application) handleUnauthorizedResponse(w http.ResponseWriter, r *http.Request, fallback doWhenUnauthorized) bool {
	if a.isAuthorized(w, r) {
		return false
	}

	switch fallback {
	case redirectToLogin:
		http.Redirect(w, r, a.Config.Server.BaseURL+"/login", http.StatusSeeOther)
	case showUnauthorizedJSON:
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "Unauthorized"}`))
	}

	return true
}

const AUTH_REDIRECT_COOKIE_NAME = "dynacat_redirect"

func isSafeLocalPath(target string) bool {
	return strings.HasPrefix(target, "/") &&
		!strings.HasPrefix(target, "//") &&
		!strings.HasPrefix(target, "/\\")
}

func (a *application) redirectToLoginPage(w http.ResponseWriter, r *http.Request) {
	if target := r.URL.RequestURI(); isSafeLocalPath(target) {
		http.SetCookie(w, &http.Cookie{
			Name:     AUTH_REDIRECT_COOKIE_NAME,
			Value:    target,
			Expires:  time.Now().Add(OIDC_STATE_VALID_PERIOD),
			Secure:   a.isRequestHTTPS(r),
			Path:     a.Config.Server.BaseURL + "/",
			SameSite: http.SameSiteLaxMode,
			HttpOnly: true,
		})
	}
	http.Redirect(w, r, a.Config.Server.BaseURL+"/login", http.StatusSeeOther)
}

func (a *application) takeLoginRedirect(w http.ResponseWriter, r *http.Request) string {
	target := a.Config.Server.BaseURL + "/"
	if c, err := r.Cookie(AUTH_REDIRECT_COOKIE_NAME); err == nil && isSafeLocalPath(c.Value) {
		target = c.Value
	}
	http.SetCookie(w, &http.Cookie{
		Name:     AUTH_REDIRECT_COOKIE_NAME,
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		Path:     a.Config.Server.BaseURL + "/",
		HttpOnly: true,
	})
	return target
}

func (a *application) AnyAuthEnabled() bool {
	return a.OIDCEnabled || a.PasswordEnabled
}

func (a *application) handleLogoutRequest(w http.ResponseWriter, r *http.Request) {
	a.setAuthSessionCookie(w, r, "", time.Now().Add(-1*time.Hour))

	if a.OIDCEnabled && a.oidcSessions != nil {
		sessionCookie, err := r.Cookie(OIDC_SESSION_COOKIE_NAME)
		if err == nil && sessionCookie.Value != "" {
			a.oidcSessions.delete(sessionCookie.Value)
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     OIDC_SESSION_COOKIE_NAME,
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		Path:     a.Config.Server.BaseURL + "/",
		HttpOnly: true,
	})

	http.Redirect(w, r, a.Config.Server.BaseURL+"/login", http.StatusSeeOther)
}

func (a *application) setAuthSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     AUTH_SESSION_COOKIE_NAME,
		Value:    token,
		Expires:  expires,
		Secure:   a.isRequestHTTPS(r),
		Path:     a.Config.Server.BaseURL + "/",
		SameSite: http.SameSiteStrictMode,
		HttpOnly: true,
	})
}

func (a *application) handleLoginPageRequest(w http.ResponseWriter, r *http.Request) {
	if a.getAuthenticatedUser(w, r) != nil {
		http.Redirect(w, r, a.Config.Server.BaseURL+"/", http.StatusSeeOther)
		return
	}

	oidcError := r.URL.Query().Get("error")

	data := &templateData{
		App:       a,
		OIDCError: oidcError,
	}
	a.populateTemplateRequestData(&data.Request, r)

	var responseBytes bytes.Buffer
	err := loginPageTemplate.Execute(&responseBytes, data)
	if err != nil {
		slog.Error("Rendering login page failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
		return
	}

	w.Write(responseBytes.Bytes())
}
