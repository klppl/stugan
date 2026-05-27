package server

import (
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
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
	p.add(webpush.Subscription{Endpoint: "https://push.example/abc"})

	// A second manager over the same dir reuses the keys and subscriptions.
	p2, err := newPushManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p2.pubKey != p.pubKey || p2.privKey != p.privKey {
		t.Error("VAPID keys not persisted across reload")
	}
	if _, ok := p2.subs["https://push.example/abc"]; !ok {
		t.Error("subscription not persisted across reload")
	}

	p2.remove("https://push.example/abc")
	p3, _ := newPushManager(dir)
	if len(p3.subs) != 0 {
		t.Errorf("subscription not removed; have %d", len(p3.subs))
	}
}

func TestPushDisabledWhenNoDir(t *testing.T) {
	p, err := newPushManager("")
	if err != nil || p != nil {
		t.Fatalf("empty dir should disable push: p=%v err=%v", p, err)
	}
}
