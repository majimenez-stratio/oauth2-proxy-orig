package jwt

import (
	"crypto/rsa"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/options"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/sessions"
	pkgcookies "github.com/oauth2-proxy/oauth2-proxy/v7/pkg/cookies"
)

// Ensure CookieSessionStore implements the interface
var _ sessions.SessionStore = &SessionStore{}

// SessionStore is an implementation of the sessions.SessionStore
// interface that stores sessions in client side cookies
type SessionStore struct {
	Cookie *options.Cookie
	JWTKey *rsa.PrivateKey
}

// NewJWTSessionStore initialises a new instance of the SessionStore from
// the configuration given
func NewJWTSessionStore(opts *options.SessionOptions, cookieOpts *options.Cookie) (sessions.SessionStore, error) {
	signKey, err := parseJWTKey(opts.JWT)
	if err != nil {
		return nil, err
	}

	return &SessionStore{
		Cookie: cookieOpts,
		JWTKey: signKey,
	}, nil
}

func parseJWTKey(o options.JWTStoreOptions) (*rsa.PrivateKey, error) {
	switch {
	case o.JWTKey != "" && o.JWTKeyFile != "":
		return nil, fmt.Errorf("cannot set both jwt-session-key and jwt-session-key-file options")
	case o.JWTKey == "" && o.JWTKeyFile == "":
		return nil, fmt.Errorf("jwt session store requires a private key for signing JWTs")
	case o.JWTKey != "":
		// The JWT Key is in the commandline argument
		signKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(o.JWTKey))
		if err != nil {
			return nil, err
		}
		return signKey, nil
	// o.JWTKeyFile != "":
	default:
		// The JWT key is in the filesystem
		keyData, err := ioutil.ReadFile(o.JWTKeyFile)
		if err != nil {
			return nil, err
		}
		signKey, err := jwt.ParseRSAPrivateKeyFromPEM(keyData)
		if err != nil {
			return nil, err
		}
		return signKey, nil
	}
}

// Save takes a sessions.SessionState and stores the information from it
// within Cookies set on the HTTP response writer
func (s *SessionStore) Save(rw http.ResponseWriter, req *http.Request, ss *sessions.SessionState) error {
	if ss.CreatedAt == nil || ss.CreatedAt.IsZero() {
		now := time.Now()
		ss.CreatedAt = &now
	}

	// create and sign jwt
	token, err := s.tokenFromSession(ss)
	if err != nil {
		return err
	}

	// create and set cookie
	c := s.makeCookie(req, s.Cookie.Name, token, s.Cookie.Expire, *ss.CreatedAt)
	http.SetCookie(rw, c)

	return nil
}

// Claims are the jwt claims structure
type Claims struct {
	jwt.StandardClaims
	UID    string   `json:"uid"`
	CN     string   `json:"cn"`
	Mail   string   `json:"mail"`
	Tenant string   `json:"tenant"`
	Groups []string `json:"groups"`
}

func (s *SessionStore) tokenFromSession(ss *sessions.SessionState) (string, error) {
	claims := Claims{
		StandardClaims: jwt.StandardClaims{
			NotBefore: ss.CreatedAt.Unix(),
			ExpiresAt: ss.CreatedAt.Add(time.Hour * 6).Unix(),
		},
		UID:    ss.User,
		CN:     ss.PreferredUsername,
		Mail:   ss.Email,
		Tenant: ss.Tenant,
		Groups: ss.Groups,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err := token.SignedString(s.JWTKey)
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

func (s *SessionStore) makeCookie(req *http.Request, name string, value string, expiration time.Duration, now time.Time) *http.Cookie {
	return pkgcookies.MakeCookieFromOptions(
		req,
		name,
		value,
		s.Cookie,
		expiration,
		now,
	)
}

// Load reads sessions.SessionState information from Cookies within the
// HTTP request object
func (s *SessionStore) Load(req *http.Request) (*sessions.SessionState, error) {
	// get cookie
	c, err := req.Cookie(s.Cookie.Name)
	if err != nil {
		return nil, err
	}

	// validate and get session from token
	return s.sessionFromToken(c.Value)
}

func (s *SessionStore) sessionFromToken(tokenString string) (*sessions.SessionState, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return &s.JWTKey.PublicKey, nil
	})

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		nbf := time.Unix(claims.NotBefore, 0)
		exp := time.Unix(claims.ExpiresAt, 0)
		return &sessions.SessionState{
			CreatedAt:         &nbf,
			ExpiresOn:         &exp,
			User:              claims.UID,
			PreferredUsername: claims.CN,
			Email:             claims.Mail,
			Tenant:            claims.Tenant,
			Groups:            claims.Groups,
		}, nil
	}
	return nil, err
}

// Clear clears any saved session information by writing a cookie to
// clear the session
func (s *SessionStore) Clear(rw http.ResponseWriter, req *http.Request) error {
	c, err := req.Cookie(s.Cookie.Name)
	if err != nil {
		return err
	}
	clearCookie := s.makeCookie(req, c.Name, "", time.Hour*-1, time.Now())
	http.SetCookie(rw, clearCookie)
	return nil
}