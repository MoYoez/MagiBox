package account

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func setupHTTPService(t *testing.T) (*bindingStore, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	store, _ := testStore(t, now)
	current = &service{
		store: store,
		client: &fakeIdentityClient{exchangeGrant: verifiedGrant{
			Issuer: store.issuer, Subject: "subject-http",
			Permissions:     []string{"magi:auth:aff", "magi:bundle:write"},
			AccessExpiresAt: now.Add(10 * time.Minute), RefreshToken: "refresh-http",
		}},
		now:      func() time.Time { return now },
		startURL: "https://app.example/auth/oidc/start",
	}
	t.Cleanup(func() { current = nil })
	return store, now
}

func TestStartHandlerRequiresConfirmationBeforeRedirect(t *testing.T) {
	store, now := setupHTTPService(t)
	ticket, err := store.issueTicket(telegramAccount{ID: 12345}, now)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/start?ticket="+ticket, nil)
	startHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "12345") || !strings.Contains(body, "confirm=1") {
		t.Fatalf("confirmation page = %s", body)
	}
	if rec.Header().Get("Location") != "" {
		t.Fatal("must not redirect before the user confirms")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/auth/oidc/start?ticket="+ticket+"&confirm=1", nil)
	startHandler(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("confirm status = %d", rec.Code)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "https://issuer.example/authorize") {
		t.Fatalf("location = %q", location)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != transactionCookieName || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("transaction cookie = %#v", cookies)
	}
}

func TestStartHandlerShowsTelegramName(t *testing.T) {
	store, now := setupHTTPService(t)
	ticket, err := store.issueTicket(telegramAccount{ID: 12345, Name: "小林", Username: "alice01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/start?ticket="+ticket, nil)
	startHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "小林") || !strings.Contains(body, "@alice01") || !strings.Contains(body, "12345") {
		t.Fatalf("confirmation page = %s", body)
	}
}

func TestStartHandlerRejectsReplayAndWrongMethod(t *testing.T) {
	store, now := setupHTTPService(t)
	ticket, err := store.issueTicket(telegramAccount{ID: 12345}, now)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/start?ticket="+ticket+"&confirm=1", nil)
	startHandler(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("first confirm status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	startHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("replayed ticket status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/auth/oidc/start?ticket="+ticket, nil)
	startHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", rec.Code)
	}
}

func TestCallbackHandlerRequiresMatchingStateCookie(t *testing.T) {
	setupHTTPService(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?state=abc&code=def", nil)
	callbackHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing cookie status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?state=abc&code=def", nil)
	req.AddCookie(&http.Cookie{Name: transactionCookieName, Value: "other"})
	callbackHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched cookie status = %d", rec.Code)
	}
}

func TestCallbackHandlerBindsAfterSuccessfulExchange(t *testing.T) {
	store, now := setupHTTPService(t)
	ticket, err := store.issueTicket(telegramAccount{ID: 12345}, now)
	if err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRecorder()
	startHandler(start, httptest.NewRequest(http.MethodGet, "/auth/oidc/start?ticket="+ticket+"&confirm=1", nil))
	if start.Code != http.StatusFound {
		t.Fatalf("start status = %d", start.Code)
	}
	cookie := start.Result().Cookies()[0]
	state := cookie.Value

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?state="+state+"&code=auth-code", nil)
	req.AddCookie(cookie)
	callbackHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "auth:aff") || strings.Contains(rec.Body.String(), "bundle") {
		t.Fatalf("callback body = %s", rec.Body.String())
	}
	cleared := false
	for _, item := range rec.Result().Cookies() {
		if item.Name == transactionCookieName && item.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("successful callback must clear the transaction cookie")
	}
	item, ok := store.binding(12345)
	if !ok || len(item.Permissions) != 1 || item.Permissions[0] != "auth:aff" {
		t.Fatalf("stored binding = %#v", item)
	}
}

func TestWritePageEscapesHTML(t *testing.T) {
	rec := httptest.NewRecorder()
	writePage(rec, http.StatusBadRequest, "<script>alert(1)</script>", "<img src=x>")
	body := rec.Body.String()
	if strings.Contains(body, "<script>") || strings.Contains(body, "<img src=x>") {
		t.Fatalf("unescaped HTML: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("expected escaped title: %s", body)
	}
}

func TestBindPagesRenderMagiBrand(t *testing.T) {
	rec := httptest.NewRecorder()
	writePage(rec, http.StatusOK, "ERP 账户绑定成功", "现在可以关闭页面并回到 Telegram。")
	body := rec.Body.String()
	if !strings.Contains(body, "Mooding~") || !strings.Contains(body, `class="ok"`) {
		t.Fatalf("success page = %s", body)
	}
	rec = httptest.NewRecorder()
	writeConfirmPage(rec, "ticket-value", telegramAccount{ID: 12345, Name: "小林", Username: "alice01"})
	body = rec.Body.String()
	if !strings.Contains(body, "Mooding~") || !strings.Contains(body, "confirm=1") ||
		!strings.Contains(body, "确认并前往 ERP 登录") || !strings.Contains(body, "12345") ||
		!strings.Contains(body, "小林") || !strings.Contains(body, "@alice01") {
		t.Fatalf("confirm page = %s", body)
	}
}

func TestWriteConfirmPageEscapesTelegramName(t *testing.T) {
	rec := httptest.NewRecorder()
	writeConfirmPage(rec, "ticket-value", telegramAccount{
		ID: 12345, Name: `<img src=x>`, Username: `alice"><script>`,
	})
	body := rec.Body.String()
	if strings.Contains(body, "<img src=x>") || strings.Contains(body, `<script>`) {
		t.Fatalf("unescaped Telegram fields: %s", body)
	}
	if !strings.Contains(body, "&lt;img src=x&gt;") {
		t.Fatalf("expected escaped name: %s", body)
	}
}
