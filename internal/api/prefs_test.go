package api

import (
	"encoding/json"
	"testing"
)

// Language and theme are stored with the account and come back with the
// session, in any browser (SPEC §8.1).
func TestAccountPreferences(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	e.do(call{method: "PUT", path: "/api/v1/admin/users/alice", body: `{"role":"reader","password":"a-long-enough-pw"}`})
	e.do(call{method: "PUT", path: "/api/v1/admin/users/root", body: `{"role":"admin","password":"a-long-enough-pw"}`})
	e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"local"}`})
	cookie, csrf, _ := e.login("alice", "a-long-enough-pw")
	if rec := e.do(call{method: "PUT", path: "/api/v1/me/prefs", body: `{"lang":"de","theme":"dark"}`, cookie: cookie, csrf: csrf}); rec.Code != 400 {
		t.Errorf("unknown language accepted: %d", rec.Code)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/me/prefs", body: `{"lang":"en","theme":"dark"}`, cookie: cookie, csrf: csrf}); rec.Code != 204 {
		t.Fatalf("save prefs: %d %s", rec.Code, rec.Body.String())
	}
	other, _, _ := e.login("alice", "a-long-enough-pw") // another browser
	var info struct {
		Prefs struct{ Lang, Theme string } `json:"prefs"`
	}
	json.Unmarshal(e.do(call{method: "GET", path: "/api/v1/session", cookie: other}).Body.Bytes(), &info)
	if info.Prefs.Lang != "en" || info.Prefs.Theme != "dark" {
		t.Errorf("prefs in the session: %+v", info.Prefs)
	}
}
