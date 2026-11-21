package desksync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestASetAsideRegisterIsKeptAndCanComeBack(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "banyan.db")
	newRegister(t, dbPath)
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return at }

	if done, err := ApplyPendingSetAside(dbPath, clock); done || err != nil {
		t.Fatalf("nothing asked: %v %v", done, err)
	}
	if err := RequestSetAside(dbPath); err != nil {
		t.Fatal(err)
	}
	if done, err := ApplyPendingSetAside(dbPath, clock); !done || err != nil {
		t.Fatalf("set aside: %v %v", done, err)
	}
	if exists(dbPath) || exists(dbPath+".set-aside") {
		t.Fatal("the register or the marker is still in place")
	}
	aside := ListSetAside(dbPath)
	if len(aside) != 1 || aside[0].Name != "banyan.db.before-read-2026-11-19T10-00-00-000Z" {
		t.Fatalf("set aside: %+v", aside)
	}

	// A new, empty register is made by the desk; the old one can come back.
	newRegister(t, dbPath)
	if err := RequestBringBack(dbPath, "banyan.db.before-read-nope"); err == nil {
		t.Fatal("an unknown name was accepted")
	}
	if err := RequestBringBack(dbPath, aside[0].Name); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Hour)
	if done, err := ApplyPendingBringBack(dbPath, clock); !done || err != nil {
		t.Fatalf("bring back: %v %v", done, err)
	}
	if !exists(dbPath) || exists(filepath.Join(dir, aside[0].Name)) || !exists(dbPath+".before-bring-back-2026-11-19T11-00-00-000Z") {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("files after bringing back: %v", entries)
	}
}
