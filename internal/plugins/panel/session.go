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
	sessionTTL = 12 * time.Hour
)

// signingKey derives the HMAC key from the panel code, so rotating PANEL_CODE
// invalidates existing sessions.
func signingKey(code string) []byte {
	sum := sha256.Sum256([]byte("magibox-panel:" + code))
	return sum[:]
}

// issueSession returns a signed "<expUnix>.<hmac>" token valid for sessionTTL.
func issueSession(code string, now time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(now.Add(sessionTTL).Unix(), 10)))
	return payload + "." + sign(code, payload)
}

// validSession reports whether token is well-formed, correctly signed for the
// current code, and unexpired.
func validSession(code, token string, now time.Time) bool {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	if !hmac.Equal([]byte(sig), []byte(sign(code, payload))) {
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

func sign(code, payload string) string {
	mac := hmac.New(sha256.New, signingKey(code))
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

// authed reports whether the request carries a valid session for code.
func authed(r *http.Request, code string) bool {
	ck, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return validSession(code, ck.Value, time.Now())
}
