package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
)

// Every route but health and sign-in needs credentials: a run's token for
// the adapter's callbacks, a signed-in user for everything else.
func TestEveryRouteNeedsCredentials(t *testing.T) {
	h := &Handlers{CallbackKey: []byte("k")}
	mux := h.Mux()
	seen := map[string]bool{}
	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, r := range h.Routes() {
		if seen[r.Pattern] {
			t.Errorf("%s registered twice", r.Pattern)
		}
		seen[r.Pattern] = true
		if r.Access == Public {
			continue
		}
		method, path, _ := strings.Cut(r.Pattern, " ")
		req := httptest.NewRequest(method, param.ReplaceAllString(path, "x"), nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s without credentials: %d, want 401", r.Pattern, w.Code)
		}
	}
	for _, p := range []string{"GET /health", "POST /auth/login"} {
		if !seen[p] {
			t.Errorf("%s missing", p)
		}
	}
	public := 0
	for _, r := range h.Routes() {
		if r.Access == Public {
			public++
		}
	}
	if public != 2 {
		t.Errorf("%d public routes, want exactly health and sign-in", public)
	}
	for _, r := range h.Routes() {
		if (r.Pattern == "POST /users" || r.Pattern == "GET /users" || r.Pattern == "POST /agents/reload") && r.Access != AdminOnly {
			t.Errorf("%s must be instance admins only", r.Pattern)
		}
	}
}

func TestPasswords(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "correct horse battery") || CheckPassword(hash, "correct horse batterY") {
		t.Error("check")
	}
	if other, _ := HashPassword("correct horse battery"); other == hash {
		t.Error("salted hashes must differ")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Error("short password accepted")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=x$a$b", "$bcrypt$x$y$z$w"} {
		if CheckPassword(bad, "anything") {
			t.Errorf("garbled hash %q accepted", bad)
		}
	}
}

// --- in-memory stores ---

type memUsers struct{ m map[string]*domain.User }

func (s *memUsers) Create(_ context.Context, u *domain.User) error {
	for _, x := range s.m {
		if x.Name == u.Name {
			return errors.New("dup")
		}
	}
	u.ID = "usr-" + u.Name
	s.m[u.ID] = u
	return nil
}
func (s *memUsers) Get(_ context.Context, id string) (*domain.User, error) {
	if u, ok := s.m[id]; ok {
		return u, nil
	}
	return nil, errors.New("no")
}
func (s *memUsers) GetByName(_ context.Context, n string) (*domain.User, error) {
	for _, u := range s.m {
		if u.Name == n {
			return u, nil
		}
	}
	return nil, errors.New("no")
}
func (s *memUsers) List(context.Context) ([]*domain.User, error) { return nil, nil }
func (s *memUsers) Count(context.Context) (int64, error)         { return int64(len(s.m)), nil }

type memSessions struct{ m map[string]*domain.Session }

func (s *memSessions) Create(_ context.Context, x *domain.Session) error { s.m[x.Hash] = x; return nil }
func (s *memSessions) Get(_ context.Context, h string) (*domain.Session, error) {
	if x, ok := s.m[h]; ok {
		return x, nil
	}
	return nil, errors.New("no")
}
func (s *memSessions) Touch(_ context.Context, h string, seen, exp time.Time) error {
	s.m[h].LastSeenAt, s.m[h].ExpiresAt = seen, exp
	return nil
}
func (s *memSessions) Delete(_ context.Context, h string) error { delete(s.m, h); return nil }

type memTokens struct{ m map[string]*domain.APIToken }

func (s *memTokens) Create(_ context.Context, t *domain.APIToken) error {
	t.ID = "tok-" + t.Name
	s.m[t.ID] = t
	return nil
}
func (s *memTokens) GetByHash(_ context.Context, h string) (*domain.APIToken, error) {
	for _, t := range s.m {
		if t.Hash == h {
			return t, nil
		}
	}
	return nil, errors.New("no")
}
func (s *memTokens) ListByUser(context.Context, string) ([]*domain.APIToken, error) { return nil, nil }
func (s *memTokens) Touch(_ context.Context, id string, at time.Time) error {
	s.m[id].LastUsedAt = &at
	return nil
}
func (s *memTokens) Revoke(_ context.Context, id, user string, at time.Time) (bool, error) {
	t, ok := s.m[id]
	if !ok || t.UserID != user || t.RevokedAt != nil {
		return false, nil
	}
	t.RevokedAt = &at
	return true, nil
}

func TestSignInSessionsTokensAndBootstrap(t *testing.T) {
	users := &memUsers{m: map[string]*domain.User{}}
	sessions := &memSessions{m: map[string]*domain.Session{}}
	tokens := &memTokens{m: map[string]*domain.APIToken{}}
	h := &Handlers{Users: users, Sessions: sessions, Tokens: tokens, BootstrapToken: "boot-secret"}
	mux := h.Mux()
	do := func(method, path, auth string, cookie *http.Cookie, body any) *httptest.ResponseRecorder {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// Bootstrap: an instance admin only while there is no user.
	if w := do("GET", "/auth/me", "wrong", nil, nil); w.Code != 401 {
		t.Errorf("wrong bootstrap: %d", w.Code)
	}
	if w := do("POST", "/auth/tokens", "boot-secret", nil, map[string]string{"name": "x"}); w.Code != 403 {
		t.Errorf("bootstrap making a token: %d", w.Code)
	}
	if w := do("POST", "/users", "boot-secret", nil, map[string]any{"name": "dana", "password": "dana-password-1"}); w.Code != 201 {
		t.Fatalf("bootstrap creates the first user: %d %s", w.Code, w.Body)
	}
	if !users.m["usr-dana"].InstanceAdmin {
		t.Error("the first user must be an instance admin")
	}
	if w := do("GET", "/auth/me", "boot-secret", nil, nil); w.Code != 401 {
		t.Errorf("bootstrap after the first user: %d", w.Code)
	}

	// Sign-in and the session cookie.
	if w := do("POST", "/auth/login", "", nil, map[string]string{"name": "dana", "password": "nope-nope-nope"}); w.Code != 401 {
		t.Errorf("wrong password: %d", w.Code)
	}
	if w := do("POST", "/auth/login", "", nil, map[string]string{"name": "nobody", "password": "dana-password-1"}); w.Code != 401 {
		t.Errorf("unknown user: %d", w.Code)
	}
	w := do("POST", "/auth/login", "", nil, map[string]string{"name": "dana", "password": "dana-password-1"})
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("sign in: %d", w.Code)
	}
	c := w.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Name != sessionCookie {
		t.Errorf("cookie flags: %+v", c)
	}
	if strings.Contains(strings.Join(func() []string {
		var ks []string
		for k := range sessions.m {
			ks = append(ks, k)
		}
		return ks
	}(), ","), c.Value) {
		t.Error("the session must be stored hashed")
	}
	if w := do("GET", "/auth/me", "", c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"via":"session"`) {
		t.Errorf("me with the cookie: %d %s", w.Code, w.Body)
	}

	// API tokens: shown once, work, revoked stop working.
	w = do("POST", "/auth/tokens", "", c, map[string]string{"name": "harness"})
	var tok struct{ Token, ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	if w.Code != 201 || !strings.HasPrefix(tok.Token, tokenPrefix) {
		t.Fatalf("create token: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/auth/me", tok.Token, nil, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"via":"token"`) {
		t.Errorf("me with the token: %d", w.Code)
	}
	if w := do("DELETE", "/auth/tokens/"+tok.ID, tok.Token, nil, nil); w.Code != 204 {
		t.Errorf("revoke: %d", w.Code)
	}
	if w := do("GET", "/auth/me", tok.Token, nil, nil); w.Code != 401 {
		t.Errorf("revoked token: %d", w.Code)
	}

	// Instance-admin routes refuse other users.
	if _, err := h.AddUser(context.Background(), "lee", "lee-password-1", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AddUser(context.Background(), "lee", "lee-password-2", "", false); !errors.Is(err, errNameTaken) {
		t.Errorf("duplicate name: %v", err)
	}
	lw := do("POST", "/auth/login", "", nil, map[string]string{"name": "lee", "password": "lee-password-1"})
	lc := lw.Result().Cookies()[0]
	if w := do("GET", "/users", "", lc, nil); w.Code != 403 {
		t.Errorf("non-admin listing users: %d", w.Code)
	}

	// Expiry, a disabled user, and sign-out.
	for _, s := range sessions.m {
		if s.UserID == "usr-lee" {
			s.ExpiresAt = time.Now().Add(-time.Second)
		}
	}
	if w := do("GET", "/auth/me", "", lc, nil); w.Code != 401 {
		t.Errorf("expired session: %d", w.Code)
	}
	now := time.Now()
	users.m["usr-dana"].DisabledAt = &now
	if w := do("GET", "/auth/me", "", c, nil); w.Code != 401 {
		t.Errorf("disabled user: %d", w.Code)
	}
	users.m["usr-dana"].DisabledAt = nil
	if w := do("POST", "/auth/logout", "", c, nil); w.Code != 204 {
		t.Errorf("sign out: %d", w.Code)
	}
	if w := do("GET", "/auth/me", "", c, nil); w.Code != 401 {
		t.Errorf("cookie after sign-out: %d", w.Code)
	}
}
