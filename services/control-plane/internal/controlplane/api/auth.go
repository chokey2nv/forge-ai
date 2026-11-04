package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"golang.org/x/crypto/argon2"
)

// Phase 13.2: people sign in. The console holds a session cookie; scripts
// send a personal API token as "Authorization: Bearer fgt_…". Until the
// first user exists, FORGE_BOOTSTRAP_TOKEN stands in for an instance admin
// so that user can be created.

const (
	sessionCookie  = "forge_session"
	tokenPrefix    = "fgt_"
	minPasswordLen = 10
	// A session is extended while used, never past this from sign-in.
	sessionMaxAge = 7 * 24 * time.Hour
)

// argon2id parameters (RFC 9106's second recommendation, lighter memory).
const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// HashPassword returns an encoded argon2id hash with a random salt.
func HashPassword(password string) (string, error) {
	if len(password) < minPasswordLen {
		return "", fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword compares a password with an encoded argon2id hash.
func CheckPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash keeps a sign-in for an unknown name as slow as for a known one.
var dummyHash, _ = HashPassword("forge-timing-equaliser")

func randomSecret(prefix string) (secret, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	secret = prefix + base64.RawURLEncoding.EncodeToString(b)
	return secret, hashSecret(secret)
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Principal is who a request acts for.
type Principal struct {
	User *domain.User
	Via  string // session | token | bootstrap
}

type principalKey struct{}

// PrincipalFrom is the signed-in caller of an authenticated route.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

var errUnauthenticated = errors.New("sign in required")

// authenticate finds who a request is from: a session cookie, an API token,
// or (only while no user exists) the bootstrap token.
func (h *Handlers) authenticate(r *http.Request) (*Principal, error) {
	ctx := r.Context()
	now := time.Now().UTC()
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" && h.Sessions != nil {
		hash := hashSecret(c.Value)
		s, err := h.Sessions.Get(ctx, hash)
		if err == nil && now.Before(s.ExpiresAt) {
			if u, err := h.activeUser(ctx, s.UserID); err == nil {
				if now.Sub(s.LastSeenAt) > time.Minute { // slide, without a write per request
					exp := now.Add(h.sessionTTL())
					if max := s.CreatedAt.Add(sessionMaxAge); exp.After(max) {
						exp = max
					}
					_ = h.Sessions.Touch(ctx, hash, now, exp)
				}
				return &Principal{User: u, Via: "session"}, nil
			}
		}
	}
	b := bearer(r)
	switch {
	case b == "":
	case strings.HasPrefix(b, tokenPrefix) && h.Tokens != nil:
		t, err := h.Tokens.GetByHash(ctx, hashSecret(b))
		if err == nil && t.RevokedAt == nil {
			if u, err := h.activeUser(ctx, t.UserID); err == nil {
				if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute {
					_ = h.Tokens.Touch(ctx, t.ID, now)
				}
				return &Principal{User: u, Via: "token"}, nil
			}
		}
	case h.BootstrapToken != "" && subtle.ConstantTimeCompare([]byte(b), []byte(h.BootstrapToken)) == 1 && h.Users != nil:
		if n, err := h.Users.Count(ctx); err == nil && n == 0 {
			return &Principal{User: &domain.User{ID: "bootstrap", Name: "bootstrap", InstanceAdmin: true}, Via: "bootstrap"}, nil
		}
	}
	return nil, errUnauthenticated
}

func (h *Handlers) activeUser(ctx context.Context, id string) (*domain.User, error) {
	u, err := h.Users.Get(ctx, id)
	if err != nil || u.DisabledAt != nil {
		return nil, errUnauthenticated
	}
	return u, nil
}

func (h *Handlers) sessionTTL() time.Duration {
	if h.SessionTTL > 0 {
		return h.SessionTTL
	}
	return 12 * time.Hour
}

// Authenticated lets only signed-in callers through.
func (h *Handlers) Authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := h.authenticate(r)
		if err != nil {
			http.Error(w, "sign in required", http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

// InstanceAdmin lets only signed-in instance admins through.
func (h *Handlers) InstanceAdmin(next http.HandlerFunc) http.HandlerFunc {
	return h.Authenticated(func(w http.ResponseWriter, r *http.Request) {
		if !PrincipalFrom(r.Context()).User.InstanceAdmin {
			http.Error(w, "instance admins only", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// RequireRole lets through signed-in users with at least role min (13.3).
func (h *Handlers) RequireRole(min domain.Role, next http.HandlerFunc) http.HandlerFunc {
	return h.Authenticated(func(w http.ResponseWriter, r *http.Request) {
		if !PrincipalFrom(r.Context()).User.Can(min) {
			http.Error(w, "this needs the "+string(min)+" role", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// actorOf is the signed-in user an action is recorded as. Every human action
// is behind authentication, so a missing principal is a wiring bug.
func actorOf(r *http.Request) *domain.User {
	if p := PrincipalFrom(r.Context()); p != nil {
		return p.User
	}
	return &domain.User{Name: "unknown"}
}

// Login: POST /auth/login {name, password} — starts a console session.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	u, err := h.Users.GetByName(ctx, strings.TrimSpace(body.Name))
	if err != nil {
		CheckPassword(dummyHash, body.Password)
		http.Error(w, "wrong name or password", http.StatusUnauthorized)
		return
	}
	if !CheckPassword(u.PasswordHash, body.Password) || u.DisabledAt != nil {
		http.Error(w, "wrong name or password", http.StatusUnauthorized)
		return
	}
	secret, hash := randomSecret("")
	now := time.Now().UTC()
	s := &domain.Session{Hash: hash, UserID: u.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(h.sessionTTL())}
	if err := h.Sessions.Create(ctx, s); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: secret, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: h.SecureCookies,
		Expires: now.Add(sessionMaxAge),
	})
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

// Logout: POST /auth/logout — ends the console session.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = h.Sessions.Delete(r.Context(), hashSecret(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: h.SecureCookies})
	w.WriteHeader(http.StatusNoContent)
}

// Me: GET /auth/me — who is signed in, and how.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"user": p.User, "via": p.Via})
}

// CreateToken: POST /auth/tokens {name} — a personal API token, shown once.
func (h *Handlers) CreateToken(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r.Context())
	if p.Via == "bootstrap" {
		http.Error(w, "the bootstrap token cannot make tokens: create a user and sign in as them", http.StatusForbidden)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Name) == "" {
		http.Error(w, "name the token (what it is for)", http.StatusBadRequest)
		return
	}
	t, secret, err := h.IssueToken(r.Context(), p.User.ID, body.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "id": t.ID, "name": t.Name, "createdAt": t.CreatedAt})
}

// IssueToken creates an API token for a user; the secret is returned once.
func (h *Handlers) IssueToken(ctx context.Context, userID, name string) (*domain.APIToken, string, error) {
	secret, hash := randomSecret(tokenPrefix)
	t := &domain.APIToken{UserID: userID, Name: strings.TrimSpace(name), Hash: hash, CreatedAt: time.Now().UTC()}
	return t, secret, h.Tokens.Create(ctx, t)
}

// ListTokens: GET /auth/tokens — the caller's tokens (never their secrets).
func (h *Handlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := h.Tokens.ListByUser(r.Context(), PrincipalFrom(r.Context()).User.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

// RevokeToken: DELETE /auth/tokens/{tid} — one of the caller's tokens.
func (h *Handlers) RevokeToken(w http.ResponseWriter, r *http.Request) {
	ok, err := h.Tokens.Revoke(r.Context(), r.PathValue("tid"), PrincipalFrom(r.Context()).User.ID, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreateUser: POST /users {name, password, email?, instanceAdmin?} — instance
// admins only. The first user is always an instance admin.
func (h *Handlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string      `json:"name"`
		Password      string      `json:"password"`
		Email         string      `json:"email,omitempty"`
		InstanceAdmin bool        `json:"instanceAdmin"`
		Role          domain.Role `json:"role,omitempty"` // default member
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	u, err := h.AddUser(r.Context(), body.Name, body.Password, body.Email, body.InstanceAdmin, body.Role)
	if errors.Is(err, errNameTaken) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

var errNameTaken = errors.New("that name is taken")

// AddUser creates a user (the HTTP endpoint and `forge users add`).
func (h *Handlers) AddUser(ctx context.Context, name, password, email string, admin bool, role domain.Role) (*domain.User, error) {
	name = strings.TrimSpace(name)
	if role == "" {
		role = domain.RoleMember
	}
	if !domain.ValidRole(role) {
		return nil, errors.New("role must be viewer, member or reviewer")
	}
	if name == "" || strings.ContainsAny(name, " \t\n/") {
		return nil, errors.New("a user needs a name without spaces or slashes")
	}
	if _, err := h.Users.GetByName(ctx, name); err == nil {
		return nil, errNameTaken
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	if n, err := h.Users.Count(ctx); err == nil && n == 0 {
		admin = true
	}
	u := &domain.User{Name: name, Email: strings.TrimSpace(email), PasswordHash: hash, InstanceAdmin: admin, Role: role, CreatedAt: time.Now().UTC()}
	if err := h.Users.Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// ListUsers: GET /users — instance admins only.
func (h *Handlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	us, err := h.Users.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, us)
}

// SetUserRole: POST /users/{uid}/role {role, instanceAdmin?} — instance admins only.
func (h *Handlers) SetUserRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role          domain.Role `json:"role"`
		InstanceAdmin *bool       `json:"instanceAdmin,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !domain.ValidRole(body.Role) {
		http.Error(w, "role must be viewer, member or reviewer", http.StatusBadRequest)
		return
	}
	u, err := h.Users.Get(r.Context(), r.PathValue("uid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	admin := u.InstanceAdmin
	if body.InstanceAdmin != nil {
		admin = *body.InstanceAdmin
	}
	if u.ID == actorOf(r).ID && !admin {
		http.Error(w, "you cannot take away your own instance admin", http.StatusConflict)
		return
	}
	if err := h.Users.SetRole(r.Context(), u.ID, body.Role, admin); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u.Role, u.InstanceAdmin = body.Role, admin
	writeJSON(w, http.StatusOK, u)
}
