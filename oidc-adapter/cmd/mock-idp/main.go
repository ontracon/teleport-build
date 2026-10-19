// A deliberately unauthenticated, local-lab-only Zitadel-compatible provider.
// It must never be connected to a production Teleport cluster.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

type flow struct {
	Params  url.Values
	Expires time.Time
	User    string
}
type provider struct {
	mu             sync.Mutex
	issuer, secret string
	key            *rsa.PrivateKey
	forms, codes   map[string]flow
	access         map[string]flow
}

func random() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(data[:])
}
func send(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
func (p *provider) cleanup() {
	for _, entries := range []map[string]flow{p.forms, p.codes, p.access} {
		for key, item := range entries {
			if !time.Now().Before(item.Expires) {
				delete(entries, key)
			}
		}
	}
}

var page = template.Must(template.New("login").Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Local Zitadel test provider</title>
<style>body{font:16px system-ui;background:#f3f5fa;color:#182239;max-width:620px;margin:8vh auto;padding:24px}main{background:white;border:1px solid #ddd;border-radius:12px;padding:32px}h1{font-size:24px}button{display:block;margin:12px 0;padding:12px 18px;font:inherit;width:100%;border:1px solid #adb7ca;border-radius:6px;cursor:pointer;background:#f8faff}button:first-of-type{background:#244ee8;color:white}small{color:#526078}</style></head>
<body><main><small>TELEPORT LOCAL LAB · MOCK PROVIDER</small><h1>Test a Zitadel login</h1><p>Choose a test identity. This provider has no password authentication and is only for the local lab.</p>
<form method="post" action="/authorize"><input type="hidden" name="flow" value="{{.}}">
<button name="user" value="operator">Sign in as operator</button>
<button name="user" value="administrator">Sign in as administrator</button>
<button name="user" value="denied">Test: no mapped role</button>
<button name="user" value="wrong-org">Test: wrong organization</button>
<button name="user" value="unverified">Test: unverified email</button>
</form></main></body></html>`))

func (p *provider) authorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://localhost:3080; frame-ancestors 'none'")
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		if q.Get("client_id") != "teleport-lab" || q.Get("response_type") != "code" || q.Get("redirect_uri") != "https://localhost:3080/v1/webapi/oidc/callback" ||
			q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			send(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		id := random()
		p.mu.Lock()
		p.cleanup()
		if len(p.forms) >= 1000 {
			p.mu.Unlock()
			http.Error(w, "lab capacity reached", 429)
			return
		}
		p.forms[id] = flow{Params: q, Expires: time.Now().Add(2 * time.Minute)}
		p.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "__Host-zitadel_mock", Value: id, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page.Execute(w, id)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	id := r.PostForm.Get("flow")
	cookie, err := r.Cookie("__Host-zitadel_mock")
	if err != nil || id == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(id)) != 1 {
		http.Error(w, "invalid login state", 403)
		return
	}
	user := r.PostForm.Get("user")
	switch user {
	case "operator", "administrator", "denied", "wrong-org", "unverified":
	default:
		http.Error(w, "invalid user", 400)
		return
	}
	p.mu.Lock()
	p.cleanup()
	item, ok := p.forms[id]
	delete(p.forms, id)
	if !ok {
		p.mu.Unlock()
		http.Error(w, "expired login", 403)
		return
	}
	item.User, item.Expires = user, time.Now().Add(time.Minute)
	code := random()
	p.codes[code] = item
	p.mu.Unlock()
	redirect, _ := url.Parse(item.Params.Get("redirect_uri"))
	query := redirect.Query()
	query.Set("code", code)
	query.Set("state", item.Params.Get("state"))
	query.Set("iss", p.issuer)
	redirect.RawQuery = query.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusSeeOther)
}

func (p *provider) claims(item flow) map[string]any {
	role, org := "operator", "456"
	if item.User == "administrator" {
		role = "administrator"
	}
	if item.User == "denied" {
		role = "unmapped"
	}
	if item.User == "wrong-org" {
		org = "999"
	}
	now := time.Now().Unix()
	return map[string]any{"iss": p.issuer, "sub": "lab-" + item.User, "aud": []string{"teleport-lab", "123"}, "azp": "teleport-lab",
		"iat": now, "exp": now + 300, "auth_time": now, "nonce": item.Params.Get("nonce"), "amr": []string{"pwd", "mfa"},
		"email": item.User + "@lab.invalid", "email_verified": item.User != "unverified",
		"preferred_username": item.User, "name": "Lab " + item.User, "nickname": item.User,
		"groups":                                []string{"123:" + role},
		"urn:zitadel:iam:org:project:123:roles": map[string]map[string]string{role: {org: "lab.invalid"}}}
}

func (p *provider) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok || id != "teleport-lab" || subtle.ConstantTimeCompare([]byte(secret), []byte(p.secret)) != 1 {
		send(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		send(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	p.mu.Lock()
	p.cleanup()
	item, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	challenge := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != item.Params.Get("redirect_uri") ||
		subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(challenge[:])), []byte(item.Params.Get("code_challenge"))) != 1 {
		send(w, 400, map[string]string{"error": "invalid_grant"})
		return
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key}, (&jose.SignerOptions{}).WithHeader("kid", "lab-key"))
	if err != nil {
		w.WriteHeader(500)
		return
	}
	data, _ := json.Marshal(p.claims(item))
	signed, err := signer.Sign(data)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	jwt, err := signed.CompactSerialize()
	if err != nil {
		w.WriteHeader(500)
		return
	}
	access := random()
	item.Expires = time.Now().Add(5 * time.Minute)
	p.mu.Lock()
	p.access[access] = item
	p.mu.Unlock()
	send(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 300, "id_token": jwt})
}

func main() {
	secret, err := os.ReadFile(os.Getenv("MOCK_SECRET_FILE"))
	if err != nil {
		log.Fatal("mock client secret file unavailable")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	p := &provider{issuer: os.Getenv("MOCK_ISSUER"), secret: strings.TrimSpace(string(secret)), key: key,
		forms: make(map[string]flow), codes: make(map[string]flow), access: make(map[string]flow)}
	if p.issuer != "https://localhost:9443" {
		log.Fatal("mock provider only supports the local lab issuer")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, map[string]any{"issuer": p.issuer, "authorization_endpoint": p.issuer + "/authorize", "token_endpoint": p.issuer + "/token",
			"userinfo_endpoint": p.issuer + "/userinfo", "jwks_uri": p.issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic"}, "code_challenge_methods_supported": []string{"S256"}})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "lab-key", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", p.authorize)
	mux.HandleFunc("/token", p.token)
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		p.mu.Lock()
		p.cleanup()
		item, ok := p.access[token]
		p.mu.Unlock()
		if !ok {
			w.WriteHeader(401)
			return
		}
		claims := p.claims(item)
		// Exercise missing-profile fallback separately from signed ID claims.
		claims["given_name"], claims["family_name"], claims["locale"] = "Lab", item.User, "en"
		send(w, 200, claims)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "Local mock IdP. Start login at https://localhost:3080.\n")
	})
	server := &http.Server{Addr: ":9443", Handler: mux, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	log.Println("local mock OIDC provider listening on :9443")
	log.Fatal(server.ListenAndServeTLS(os.Getenv("MOCK_CERT_FILE"), os.Getenv("MOCK_KEY_FILE")))
}
