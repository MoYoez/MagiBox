package panel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	cookieName = "mb_panel"
	sessionTTL = 30 * 24 * time.Hour // one month
)

// issueSession returns a signed "<expUnix>.<hmac>" token valid for sessionTTL.
func issueSession(now time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(now.Add(sessionTTL).Unix(), 10)))
	return payload + "." + sign(payload)
}

// validSession reports whether token is well-formed, correctly signed with the
// current secret, and unexpired.
func validSession(token string, now time.Time) bool {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	if !hmac.Equal([]byte(sig), []byte(sign(payload))) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	exp, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return false
	}
	return now.Unix() < exp
}

func sign(payload string) string {
	mac := hmac.New(sha256.New, secret())
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// setSessionCookie writes the session cookie; Secure is set when the request
// arrived over TLS (directly or via a proxy).
func setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/panel",
		HttpOnly: true,
		Secure:   isTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func isTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// authed reports whether the request carries a valid session.
func authed(r *http.Request) bool {
	ck, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return validSession(ck.Value, time.Now())
}
