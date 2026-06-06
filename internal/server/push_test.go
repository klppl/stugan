package server

import (
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/klippelism/stugan/internal/core"
)

func TestPushManagerPersistence(t *testing.T) {
	dir := t.TempDir()

	p, err := newPushManager(dir)
	if err != nil {
		t.Fatalf("newPushManager: %v", err)
	}
	if p.pubKey == "" || p.privKey == "" {
		t.Fatal("VAPID keys not generated")
	}
	p.add("alice", webpush.Subscription{Endpoint: "https://push.example/abc"})

	// A second manager over the same dir reuses the keys and subscriptions.
	p2, err := newPushManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p2.pubKey != p.pubKey || p2.privKey != p.privKey {
		t.Error("VAPID keys not persisted across reload")
	}
	if _, ok := p2.subs["alice"]["https://push.example/abc"]; !ok {
		t.Error("subscription not persisted across reload")
	}

	p2.remove("alice", "https://push.example/abc")
	p3, _ := newPushManager(dir)
	if len(p3.subs["alice"]) != 0 {
		t.Errorf("subscription not removed; have %d", len(p3.subs["alice"]))
	}
}

func TestIsNotifyDM(t *testing.T) {
	cases := []struct {
		name string
		m    core.Message
		want bool
	}{
		{"dm privmsg", core.Message{Buffer: "alice", Kind: core.MsgPrivmsg}, true},
		{"dm action", core.Message{Buffer: "alice", Kind: core.MsgAction}, true},
		{"dm notice", core.Message{Buffer: "alice", Kind: core.MsgNotice}, true},
		{"channel privmsg", core.Message{Buffer: "#chan", Kind: core.MsgPrivmsg}, false},
		{"status buffer", core.Message{Buffer: core.StatusBuffer, Kind: core.MsgNotice}, false},
		{"dm join (non-conversational)", core.Message{Buffer: "alice", Kind: core.MsgJoin}, false},
	}
	for _, c := range cases {
		if got := isNotifyDM(c.m); got != c.want {
			t.Errorf("%s: isNotifyDM = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPushDisabledWhenNoDir(t *testing.T) {
	p, err := newPushManager("")
	if err != nil || p != nil {
		t.Fatalf("empty dir should disable push: p=%v err=%v", p, err)
	}
}
