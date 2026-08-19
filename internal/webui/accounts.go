package webui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/internal/store"
)

// Accounts turns the people table into logins.
//
// Two decisions shape everything here.
//
// First, a session is a SIGNED COOKIE, not a stored row. A login that
// writes a record per browser tab turns an ordinary working day into rows
// nobody prunes, and the one thing the record would buy — revoking a single
// session — is better served by changing the password, which invalidates
// every cookie at once because the signature is taken over the password
// hash.
//
// Second, an invitation is a bearer credential: whoever holds the link can
// become a user. So it is stored only as a hash, returned exactly once, and
// expires on its own. A link pasted into a chat channel must stop working
// even if nobody remembers it is there.
type Accounts struct {
	DB *store.DB
	// Key signs session cookies. Derived from the same material as preview
	// tickets, so a deployment has one secret to protect rather than two.
	Key []byte
}

const (
	// sessionTTL is how long a login lasts WITHOUT USE.
	//
	// It used to be twelve hours absolute, and the comment here said the
	// point was that a forgotten laptop stops being a way in within the
	// week. Absolute expiry does not achieve that — it logs everybody out
	// every twelve hours whether they are working or not, which is a daily
	// interruption for the people who are here and no additional protection
	// against the laptop that is not.
	//
	// Sliding achieves the stated goal directly: somebody using the console
	// is never asked again, and a session nobody has touched for a week is
	// gone. A password change still ends every session instantly, because
	// the signature covers the hash.
	sessionTTL = 7 * 24 * time.Hour
	// renewWithin re-issues a cookie once it is past halfway, so the common
	// request does no extra work and an active session never runs out.
	renewWithin = sessionTTL / 2
	inviteTTL   = 7 * 24 * time.Hour
	// minPassword is a length, not a character-class rule. Length is what
	// actually resists guessing; the rules mostly produce Password1! and a
	// sticky note.
	minPassword = 10
)

// --- passwords -------------------------------------------------------

// hashPassword returns an argon2id string, parameters included, so a future
// change of cost can be rolled out without invalidating existing hashes.
func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const t, m, p = 3, 64 * 1024, 2
	k := argon2.IDKey([]byte(pw), salt, t, m, p, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", m, t, p,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(k)), nil
}

// verifyPassword re-derives with the stored parameters and compares in
// constant time, so a wrong password cannot be narrowed down by how quickly
// it was rejected.
func verifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t, p int
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func principalOf(u store.User) identity.Principal {
	return identity.Principal{
		Subject: identity.UserSubject(u.Email),
		Name:    u.Name,
		Email:   u.Email,
		Kind:    identity.KindUser,
		Admin:   u.Admin,
	}
}

// Verify checks an email and password.
func (a *Accounts) Verify(ctx context.Context, email, pw string) (identity.Principal, bool) {
	u, hash, err := a.DB.UserByEmail(ctx, email)
	if err != nil {
		// Hash anyway. Otherwise an unknown address answers measurably
		// faster than a known one with the wrong password, and the login
		// form becomes a way to find out who works here.
		_, _ = hashPassword(pw)
		return identity.Principal{}, false
	}
	if u.Disabled || !verifyPassword(hash, pw) {
		return identity.Principal{}, false
	}
	return principalOf(u), true
}

// --- session cookies -------------------------------------------------

// Mint returns a signed cookie value for this person.
func (a *Accounts) Mint(ctx context.Context, email string) (string, error) {
	_, hash, err := a.DB.UserByEmail(ctx, email)
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf("%s|%d", store.NormalizeEmail(email), time.Now().Add(sessionTTL).Unix())
	return body + "." + a.sign(body, hash), nil
}

// sign covers the identity, the expiry AND the password hash. Including the
// hash is what makes a password change end every session that exists,
// without anything having to remember that those sessions exist.
func (a *Accounts) sign(body, pwHash string) string {
	mac := hmac.New(sha256.New, a.Key)
	mac.Write([]byte(body))
	mac.Write([]byte{0})
	mac.Write([]byte(pwHash))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// FromCookie validates a session cookie, refusing anything it cannot fully
// verify: expired, tampered with, or signed against a password that has
// since changed.
func (a *Accounts) FromCookie(ctx context.Context, value string) (identity.Principal, bool) {
	p, ok, _ := a.fromCookie(ctx, value)
	return p, ok
}

// fromCookie also reports when the cookie is old enough to be worth
// re-issuing, so the caller can slide the window forward.
func (a *Accounts) fromCookie(ctx context.Context, value string) (identity.Principal, bool, bool) {
	// Split at the LAST dot, not the first: the body starts with an email
	// address and every address anybody actually has contains dots, so
	// cutting at the first one shredded the value and refused every cookie
	// this code had just issued. The signature is base64url and contains
	// none, which is what makes the last dot unambiguous.
	i := strings.LastIndexByte(value, '.')
	if i < 0 {
		return identity.Principal{}, false, false
	}
	body, sig := value[:i], value[i+1:]
	email, expS, ok := strings.Cut(body, "|")
	if !ok {
		return identity.Principal{}, false, false
	}
	exp, err := strconv.ParseInt(expS, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return identity.Principal{}, false, false
	}
	u, hash, err := a.DB.UserByEmail(ctx, email)
	if err != nil || u.Disabled {
		return identity.Principal{}, false, false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(a.sign(body, hash))) != 1 {
		return identity.Principal{}, false, false
	}
	// Past halfway: worth sliding the window forward. Before halfway the
	// request costs nothing extra, which is most of them.
	stale := time.Until(time.Unix(exp, 0)) < renewWithin
	return principalOf(u), true, stale
}

// --- invitations -----------------------------------------------------

func inviteHash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// Invite records an invitation and returns the one-time token that redeems
// it. The token itself is never stored: a copy sitting in the database
// would be a second way into the platform for anyone who can read it.
func (a *Accounts) Invite(ctx context.Context, email, name, by string, admin, canCreate bool) (string, error) {
	if _, err := mail.ParseAddress(email); err != nil {
		return "", fmt.Errorf("邮箱地址看起来不对")
	}
	if _, _, err := a.DB.UserByEmail(ctx, email); err == nil {
		return "", fmt.Errorf("这个邮箱已经有账号了")
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	err := a.DB.CreateInvite(ctx, inviteHash(tok), store.Invite{
		Email: email, Name: name, Admin: admin, By: by,
		// An administrator can always create; recording it explicitly keeps
		// the invitation a full description of what was granted, rather than
		// something that has to be re-derived from a role later.
		CanCreate: canCreate || admin,
		Expires:   time.Now().Add(inviteTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	return tok, nil
}

// Redeem turns an invitation into an account.
func (a *Accounts) Redeem(ctx context.Context, tok, name, pw string) (identity.Principal, error) {
	in, err := a.DB.Invite(ctx, inviteHash(tok))
	if err != nil {
		return identity.Principal{}, fmt.Errorf("邀请链接无效或已过期")
	}
	if len([]rune(pw)) < minPassword {
		return identity.Principal{}, fmt.Errorf("密码至少 %d 位", minPassword)
	}
	hash, err := hashPassword(pw)
	if err != nil {
		return identity.Principal{}, err
	}
	if name == "" {
		name = in.Name
	}
	uid := identity.Principal{Subject: identity.UserSubject(in.Email)}.ID()
	if err := a.DB.CreateUser(ctx, uid, in.Email, name, hash, in.Admin, in.CanCreate); err != nil {
		return identity.Principal{}, fmt.Errorf("这个邮箱已经有账号了")
	}
	// Consume AFTER the account exists. The other order loses the
	// invitation to a failed create and leaves somebody holding a dead link
	// and no account — with nobody able to tell which happened.
	if err := a.DB.DeleteInvite(ctx, inviteHash(tok)); err != nil {
		return identity.Principal{}, err
	}
	u, _, err := a.DB.UserByEmail(ctx, in.Email)
	if err != nil {
		return identity.Principal{}, err
	}
	return principalOf(u), nil
}

// Bootstrap creates the first administrator if there are none.
//
// A deployment with an empty table has nobody who can invite anybody, so
// without this the only way in is to edit the database by hand. It runs
// once by definition: the moment one account exists, this does nothing.
func (a *Accounts) Bootstrap(ctx context.Context, email, pw string) error {
	n, err := a.DB.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	hash, err := hashPassword(pw)
	if err != nil {
		return err
	}
	uid := identity.Principal{Subject: identity.UserSubject(email)}.ID()
	return a.DB.CreateUser(ctx, uid, email, "", hash, true, true)
}

// --- HTTP ------------------------------------------------------------

func (a *Authenticator) accountLogin(w http.ResponseWriter, r *http.Request) {
	if a.Accounts == nil {
		a.tokenLogin(w, r)
		return
	}
	email := r.FormValue("email")
	// Refused BEFORE the password work, which is the whole point: the cost
	// this protects is the argon2 derivation itself.
	if a.tooManyLogins(r, email) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(loginPageHTML("尝试太频繁了,过一分钟再试"))
		return
	}
	p, ok := a.Accounts.Verify(r.Context(), email, r.FormValue("password"))
	if !ok {
		// One message for a wrong address and a wrong password. Telling
		// them apart is a way to find out who has an account here.
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(loginPageHTML("邮箱或密码不对"))
		return
	}
	value, err := a.Accounts.Mint(r.Context(), p.Email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.setSessionCookie(w, r, value, int(sessionTTL.Seconds()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *Authenticator) logout(w http.ResponseWriter, r *http.Request) {
	a.setSessionCookie(w, r, "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// PublicRoutes are reachable WITHOUT a login, because they are how a person
// who has no account yet gets one. Kept to exactly two endpoints, both of
// which require holding a valid invitation token, so "public" does not
// quietly mean "an open door".
func (h *Handler) PublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/invite", h.inviteInfo)
	mux.HandleFunc("POST /api/invite/redeem", h.inviteRedeem)
	mux.HandleFunc("GET /invite", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(invitePageHTML(r.URL.Query().Get("t")))
	})
}

// invitePageHTML is served before the console's own JavaScript exists for
// this person — they cannot load it, because it lives behind the login they
// are in the middle of creating.
func invitePageHTML(tok string) []byte {
	return []byte(`<!doctype html><meta charset=utf-8>
<title>AgentCell — 接受邀请</title>
<style>body{font:15px system-ui;display:grid;place-items:center;height:100vh;margin:0;background:#171a18;color:#e6e9e7}
form{display:flex;gap:8px;flex-direction:column;width:320px}
input,button{padding:9px;border-radius:6px;border:1px solid #333;font:inherit}
button{background:#58a17b;color:#10140f;border:0;cursor:pointer}
p{margin:0;font-size:13px;color:#9aa}</style>
<form id=f>
<h2>接受邀请</h2>
<p id=who>正在确认邀请……</p>
<input name=name placeholder="你的名字(可留空)">
<input name=password type=password placeholder="设置密码(至少 10 位)" autocomplete=new-password>
<button>创建账号并进入</button>
<p id=err style="color:#e0776a"></p>
</form>
<script>
const t = ` + "`" + tok + "`" + `;
fetch('/api/invite?t='+encodeURIComponent(t)).then(r=>r.json()).then(d=>{
  document.getElementById('who').textContent = d.error ? d.error : ('邀请给:'+d.email);
  if (d.error) document.getElementById('f').querySelector('button').disabled = true;
});
document.getElementById('f').addEventListener('submit', async e => {
  e.preventDefault();
  const f = e.target;
  const r = await fetch('/api/invite/redeem', {method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({token:t, name:f.name.value, password:f.password.value})});
  const d = await r.json();
  if (r.ok) location.href = '/'; else document.getElementById('err').textContent = d.error || '创建失败';
});
</script>`)
}

// inviteInfo tells the redeem page which address it is for, so nobody has
// to retype an address the platform already knows.
func (h *Handler) inviteInfo(w http.ResponseWriter, r *http.Request) {
	if h.Auth == nil || h.Auth.Accounts == nil {
		writeErr(w, 501, errors.New("这个部署没有开启账号体系"))
		return
	}
	in, err := h.Auth.Accounts.DB.Invite(r.Context(), inviteHash(r.URL.Query().Get("t")))
	if err != nil {
		writeErr(w, 404, errors.New("邀请链接无效或已过期"))
		return
	}
	writeJSON(w, 200, map[string]string{"email": in.Email, "name": in.Name})
}

func (h *Handler) inviteRedeem(w http.ResponseWriter, r *http.Request) {
	if h.Auth == nil || h.Auth.Accounts == nil {
		writeErr(w, 501, errors.New("这个部署没有开启账号体系"))
		return
	}
	var body struct{ Token, Name, Password string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	p, err := h.Auth.Accounts.Redeem(r.Context(), body.Token, body.Name, body.Password)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	// Log them straight in. Making somebody who just set a password type it
	// again proves nothing and loses people at the last step.
	value, err := h.Auth.Accounts.Mint(r.Context(), p.Email)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	h.Auth.setSessionCookie(w, r, value, int(sessionTTL.Seconds()))
	writeJSON(w, 200, map[string]string{"email": p.Email, "name": p.Name})
}

// createInvite is how a new person gets in: there is no self-registration,
// because this platform hands whoever holds an account a shell inside the
// cluster.
func (h *Handler) createInvite(w http.ResponseWriter, r *http.Request) {
	if h.Auth == nil || h.Auth.Accounts == nil {
		writeErr(w, 501, errors.New("这个部署没有开启账号体系"))
		return
	}
	p := identity.FromContext(r.Context())
	if !p.Admin && p.Kind != identity.KindToken {
		writeErr(w, 403, errors.New("只有管理员能邀请人"))
		return
	}
	var body struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Admin bool   `json:"admin"`
		// CanCreate grants the right to start projects, at the moment of
		// invitation rather than as a separate act afterwards.
		CanCreate bool `json:"canCreate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	tok, err := h.Auth.Accounts.Invite(r.Context(), body.Email, body.Name, p.Email, body.Admin, body.CanCreate)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	// Returned once. The platform cannot mail it — a deployment on an
	// internal network has no mail server it may assume — so whoever
	// invited them passes the link on, and if it is lost the answer is a
	// new invitation, not a lookup.
	writeJSON(w, 200, map[string]string{
		"invite":  tok,
		"path":    "/invite?t=" + tok,
		"expires": time.Now().Add(inviteTTL).UTC().Format(time.RFC3339),
	})
}

func (h *Handler) listPeople(w http.ResponseWriter, r *http.Request) {
	if h.Auth == nil || h.Auth.Accounts == nil {
		writeJSON(w, 200, []any{})
		return
	}
	users, err := h.Auth.Accounts.DB.Users(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	type view struct {
		Email    string `json:"email"`
		Name     string `json:"name,omitempty"`
		Admin    bool   `json:"admin,omitempty"`
		Disabled bool   `json:"disabled,omitempty"`
		// CanCreate is on the list because an access list that does not show
		// a grant is one nobody audits: "who here can start projects" has to
		// be answerable by looking.
		CanCreate bool `json:"canCreate,omitempty"`
	}
	out := make([]view, 0, len(users))
	for _, u := range users {
		out = append(out, view{
			Email: u.Email, Name: u.Name, Admin: u.Admin, Disabled: u.Disabled,
			CanCreate: u.CanCreate || u.Admin,
		})
	}
	writeJSON(w, 200, out)
}

// whoami lets the console show who it thinks you are — the first thing
// anybody asks when permissions surprise them.
func (h *Handler) whoami(w http.ResponseWriter, r *http.Request) {
	p := identity.FromContext(r.Context())
	writeJSON(w, 200, map[string]any{
		"email": p.Email, "name": p.Display(),
		"admin": p.Admin, "kind": string(p.Kind),
	})
}

// changePassword also ends every other session this person has open,
// because the cookie signature covers the password hash. That is the
// intended behaviour for "I think somebody has my password".
func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	if h.Auth == nil || h.Auth.Accounts == nil {
		writeErr(w, 501, errors.New("这个部署没有开启账号体系"))
		return
	}
	p := identity.FromContext(r.Context())
	if p.Kind != identity.KindUser {
		writeErr(w, 403, errors.New("只有账号登录的用户能改密码"))
		return
	}
	var body struct{ Current, New string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if _, ok := h.Auth.Accounts.Verify(r.Context(), p.Email, body.Current); !ok {
		writeErr(w, 403, errors.New("当前密码不对"))
		return
	}
	if len([]rune(body.New)) < minPassword {
		writeErr(w, 400, fmt.Errorf("新密码至少 %d 位", minPassword))
		return
	}
	hash, err := hashPassword(body.New)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := h.Auth.Accounts.DB.SetPassword(r.Context(), p.Email, hash); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Re-issue this browser's cookie against the new hash, so changing the
	// password does not log out the person who just changed it.
	value, err := h.Auth.Accounts.Mint(r.Context(), p.Email)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	h.Auth.setSessionCookie(w, r, value, int(sessionTTL.Seconds()))
	writeJSON(w, 200, map[string]string{"ok": "changed",
		"message": "改好了——你在其他地方的登录都已经失效"})
}
