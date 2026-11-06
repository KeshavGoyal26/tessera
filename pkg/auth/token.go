package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// KeyProvider resolves API keys to secrets.
type KeyProvider interface {
	// Secret returns the secret for apiKey.
	Secret(apiKey string) (secret string, ok bool)
}

// StaticKeys is a KeyProvider backed by a map.
type StaticKeys map[string]string

// Secret implements KeyProvider.
func (k StaticKeys) Secret(apiKey string) (string, bool) {
	s, ok := k[apiKey]
	return s, ok
}

// Verifier validates access tokens.
type Verifier interface {
	// Verify checks signature, algorithm and validity window and returns the claims.
	// Errors are errs.ErrUnauthenticated-class.
	Verify(token string) (*Claims, error)
}

// Leeway is the clock skew tolerated when checking nbf and exp.
const Leeway = 60 * time.Second

// DefaultTTL is the default lifetime of tokens minted by AccessToken.
const DefaultTTL = 6 * time.Hour

var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// AccessToken builds signed access tokens.
type AccessToken struct {
	apiKey, secret string
	claims         Claims
	ttl            time.Duration
	now            func() time.Time
}

// NewAccessToken starts a token signed with the given key pair.
func NewAccessToken(apiKey, secret string) *AccessToken {
	return &AccessToken{apiKey: apiKey, secret: secret, ttl: DefaultTTL, now: time.Now}
}

// SetIdentity sets the participant identity ("sub").
func (t *AccessToken) SetIdentity(id string) *AccessToken { t.claims.Subject = id; return t }

// SetName sets the display name.
func (t *AccessToken) SetName(n string) *AccessToken { t.claims.Name = n; return t }

// SetMetadata sets opaque metadata.
func (t *AccessToken) SetMetadata(m string) *AccessToken { t.claims.Metadata = m; return t }

// SetAttributes sets participant attributes.
func (t *AccessToken) SetAttributes(a map[string]string) *AccessToken {
	t.claims.Attributes = a
	return t
}

// SetGrant sets the grants.
func (t *AccessToken) SetGrant(g *RoomGrant) *AccessToken { t.claims.Grants = g; return t }

// SetValidFor sets the token lifetime.
func (t *AccessToken) SetValidFor(d time.Duration) *AccessToken { t.ttl = d; return t }

// SetID sets the "jti" claim.
func (t *AccessToken) SetID(id string) *AccessToken { t.claims.ID = id; return t }

// ToJWT signs the token.
func (t *AccessToken) ToJWT() (string, error) {
	if t.apiKey == "" || t.secret == "" {
		return "", errs.New(errs.CodeInvalidArgument, "api key and secret are required")
	}
	if t.claims.Grants != nil && t.claims.Grants.RoomJoin && t.claims.Subject == "" {
		return "", errs.New(errs.CodeInvalidArgument, "identity is required for room_join")
	}
	now := t.now()
	c := t.claims
	c.Issuer = t.apiKey
	c.NotBefore = now.Unix()
	c.Expiry = now.Add(t.ttl).Unix()
	return Sign(&c, t.secret)
}

// Sign encodes and signs claims with HS256.
func Sign(c *Claims, secret string) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signingInput := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(payload)
	return signingInput + "." + signature(signingInput, secret), nil
}

func signature(signingInput, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(signingInput))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

type hmacVerifier struct {
	keys KeyProvider
	now  func() time.Time
}

// NewVerifier returns a Verifier that accepts HS256 tokens signed by any key in keys.
func NewVerifier(keys KeyProvider) Verifier {
	return &hmacVerifier{keys: keys, now: time.Now}
}

func unauthenticated(msg string) error { return errs.New(errs.CodeUnauthenticated, "%s", msg) }

func (v *hmacVerifier) Verify(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, unauthenticated("malformed token")
	}
	hdrRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, unauthenticated("malformed token header")
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(hdrRaw, &hdr); err != nil || hdr.Alg != "HS256" {
		return nil, unauthenticated("unsupported token algorithm")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, unauthenticated("malformed token payload")
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, unauthenticated("malformed token claims")
	}
	secret, ok := v.keys.Secret(c.Issuer)
	if !ok { // same message as a bad signature: do not reveal which API keys exist
		return nil, unauthenticated("invalid token signature")
	}
	want := signature(parts[0]+"."+parts[1], secret)
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[2])) != 1 {
		return nil, unauthenticated("invalid token signature")
	}
	now := v.now()
	if c.Expiry == 0 || now.After(time.Unix(c.Expiry, 0).Add(Leeway)) {
		return nil, unauthenticated("token expired")
	}
	if c.NotBefore != 0 && now.Add(Leeway).Before(time.Unix(c.NotBefore, 0)) {
		return nil, unauthenticated("token not yet valid")
	}
	return &c, nil
}
