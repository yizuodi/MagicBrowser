package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newT(t *testing.T) *Tokens {
	t.Helper()
	return NewTokens([]byte("0123456789abcdef0123456789abcdef"))
}

func TestSignVerifyRoundTrip(t *testing.T) {
	tk := newT(t)
	token, exp := tk.Sign(PurposeSite, time.Hour)
	if time.Until(exp) <= 0 {
		t.Fatal("expiry should be in the future")
	}
	if !tk.Verify(token, PurposeSite) {
		t.Fatal("valid token rejected")
	}
	if tk.Verify(token, PurposeAdmin) {
		t.Fatal("site token accepted as admin")
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	tk := newT(t)
	token, _ := tk.Sign(PurposeAdmin, time.Hour)
	if tk.Verify(token+"x", PurposeAdmin) {
		t.Fatal("tampered token accepted")
	}
	otherTok, _ := tk.Sign(PurposeAdmin, time.Hour)
	if tk.Verify(token[:len(token)-2]+otherTok[:4], PurposeAdmin) {
		t.Fatal("spliced token accepted")
	}
	if tk.Verify("", PurposeAdmin) {
		t.Fatal("empty token accepted")
	}
	// 不同密钥签发的 token 不被接受。
	other := NewTokens([]byte("ffffffffffffffffffffffffffffffff"))
	if other.Verify(token, PurposeAdmin) {
		t.Fatal("token from different secret accepted")
	}
}

func TestVerifyExpiry(t *testing.T) {
	tk := newT(t)
	token, _ := tk.Sign(PurposeSite, -time.Minute) // 已过期
	if tk.Verify(token, PurposeSite) {
		t.Fatal("expired token accepted")
	}
}

func TestAuthorizeAndIssueCookie(t *testing.T) {
	tk := newT(t)
	tok, _ := tk.Sign(PurposeAdmin, time.Hour)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := tk.Authorize(r, PurposeAdmin); err == nil {
		t.Fatal("request without cookie should fail auth")
	}
	r.AddCookie(&http.Cookie{Name: "magic_admin", Value: tok})
	if err := tk.Authorize(r, PurposeAdmin); err != nil {
		t.Fatalf("valid cookie rejected: %v", err)
	}
	if err := tk.Authorize(r, PurposeSite); err == nil {
		t.Fatal("admin cookie accepted for site purpose")
	}

	w := httptest.NewRecorder()
	if err := tk.IssueCookie(w, PurposeSite); err != nil {
		t.Fatalf("IssueCookie: %v", err)
	}
	resp := w.Result()
	var found bool
	for _, ck := range resp.Cookies() {
		if ck.Name == "magic_site" && ck.Value != "" && ck.HttpOnly {
			found = true
		}
	}
	if !found {
		t.Fatal("site cookie not issued")
	}
}
