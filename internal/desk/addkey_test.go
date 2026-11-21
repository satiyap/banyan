package desk

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/satiyap/banyan/internal/db"
	"github.com/satiyap/banyan/internal/desksync"
)

func TestKeyRoleReadsThePrefix(t *testing.T) {
	for key, want := range map[string]string{
		"W-ABCDE-FGHJK-MNPQR-STUVW-XYZ23": "write",
		" r-abcde-fghjk ":                 "read",
		"ABCDE-FGHJK-MNPQR-STUVW-XYZ23":   "",
		"WABCD-FGHJK":                     "",
	} {
		if got := KeyRole(key); got != want {
			t.Errorf("%q: %q, want %q", key, got, want)
		}
	}
}

func TestAddingAKeySaysWhatItWillDoFirst(t *testing.T) {
	one := int64(1)
	two := int64(2)
	cases := []struct {
		name       string
		key        string
		register   *HeldRegister
		agreed     *int64
		withLeaves bool
		action     string
		connects   string
		fetches    int
		restarts   int
	}{
		{"write key, empty server: seeds it", "W-AAAAA", &HeldRegister{Held: false}, nil, true, "connect", "write/true", 0, 0},
		{"write key, server holds this Mac's copy: carries on", "W-AAAAA", &HeldRegister{Held: true, Revision: &one}, &one, true, "connect", "write/true", 0, 0},
		{"write key, server has moved on: replace", "W-AAAAA", &HeldRegister{Held: true, Revision: &two}, &one, true, "replace", "write/false", 1, 1},
		{"write key, server holds another register: replace", "W-AAAAA", &HeldRegister{Held: true, Revision: &one}, nil, false, "replace", "write/false", 1, 1},
		{"write key, old server says nothing: as before", "W-AAAAA", nil, nil, true, "connect", "write/true", 0, 0},
		{"read key, Mac has records: set aside", "R-AAAAA", &HeldRegister{Held: true, Revision: &one}, nil, true, "set-aside", "read/false", 0, 1},
		{"read key, Mac is empty: connects", "R-AAAAA", &HeldRegister{Held: true, Revision: &one}, nil, false, "connect", "read/true", 0, 0},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			link := &fakeSync{register: c.register, agreed: c.agreed}
			restarts := 0
			cfg := configFor(t, filepath.Join(work, "addkey", string(rune('a'+i))))
			app := newApp(t, cfg, Options{Sync: link, OnRestoreReady: func() { restarts++ }})
			if c.withLeaves {
				if _, err := app.db.AddBlankLeaves([]string{"000141"}, "test"); err != nil {
					t.Fatal(err)
				}
			}
			h := map[string]string{"X-Desk-Version": app.Version(), "Content-Type": "application/json"}
			body := `{"api_url":"https://banyan.invalid","user_key":"` + c.key + `"}`
			plan := parse(t, send(app, "POST", "/api/connection/plan", h, &body).Body.String()).(obj)
			if plan.text("action") != c.action {
				t.Fatalf("plan: %s", stringify(t, plan))
			}
			if c.action != "connect" {
				// Not without the page confirming that very action.
				if res := send(app, "PUT", "/api/connection", h, &body); res.Code != 409 || len(link.connected) != 0 {
					t.Fatalf("unconfirmed: %d, connected %v", res.Code, link.connected)
				}
				body = `{"api_url":"https://banyan.invalid","user_key":"` + c.key + `","confirm":"` + c.action + `"}`
			}
			if res := send(app, "PUT", "/api/connection", h, &body); res.Code != 200 {
				t.Fatalf("connect: %d %s", res.Code, res.Body)
			}
			if len(link.connected) != 1 || link.connected[0] != c.connects || link.fetched != c.fetches || restarts != c.restarts {
				t.Errorf("connected %v, fetched %d, restarts %d", link.connected, link.fetched, restarts)
			}
			marked := exists(cfg.DBPath + ".set-aside")
			if marked != (c.action == "set-aside") {
				t.Errorf("set-aside marker: %v", marked)
			}
		})
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestLeavesFromASetAsideRegisterAreNeverOfferedAgain(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "banyan.db.before-restore-x")
	was, err := db.Open(old, db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := was.AddBlankLeaves([]string{"000141", "000142"}, "used here"); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(dir, "banyan.db")
	now, err := db.Open(current, db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := now.AddBlankLeaves([]string{"000142"}, "already known"); err != nil {
		t.Fatal(err)
	}
	leaves, err := desksync.LeavesIn(old)
	if err != nil || len(leaves) != 2 {
		t.Fatalf("leaves %v %v", leaves, err)
	}
	if err := os.WriteFile(desksync.CarryPath(current), []byte(`{"from":"banyan.db.before-restore-x","leaves":["000141","000142"]}`), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := carryLeaves(now, current); err != nil {
		t.Fatal(err)
	}
	used, err := now.UsedNumbers()
	if err != nil || !used["000141"] || !used["000142"] || len(used) != 2 {
		t.Fatalf("used %v %v", used, err)
	}
	if exists(desksync.CarryPath(current)) {
		t.Fatal("the carry file was left behind")
	}
	// Running again does nothing.
	if err := carryLeaves(now, current); err != nil {
		t.Fatal(err)
	}
}
