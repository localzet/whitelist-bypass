package controlplane

import (
	"testing"
	"time"
)

func TestManagerCreateIsIdempotentByUserAndRequest(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	manager := NewManager(Config{WorkTTL: time.Minute})
	manager.SetClockForTest(func() time.Time { return now })

	first, removed, err := manager.CreateOrReplace(CreateSessionInput{
		UserID:    "user-1",
		RequestID: "req-1",
		Kind:      SessionKindWork,
		EgressID:  "de-fra-1",
		JoinLink:  "call-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %d, want 0", len(removed))
	}

	second, removed, err := manager.CreateOrReplace(CreateSessionInput{
		UserID:    "user-1",
		RequestID: "req-1",
		Kind:      SessionKindWork,
		EgressID:  "nl-ams-1",
		JoinLink:  "call-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.JoinLink != first.JoinLink || len(removed) != 0 {
		t.Fatalf("idempotent result = %+v removed=%d, want %+v removed=0", second, len(removed), first)
	}
}

func TestManagerKeepsOneWorkSessionPerUser(t *testing.T) {
	manager := NewManager(Config{})

	first, _, err := manager.CreateOrReplace(CreateSessionInput{
		UserID:    "user-1",
		RequestID: "req-1",
		Kind:      SessionKindWork,
		EgressID:  "de-fra-1",
		JoinLink:  "call-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, removed, err := manager.CreateOrReplace(CreateSessionInput{
		UserID:    "user-1",
		RequestID: "req-2",
		Kind:      SessionKindWork,
		EgressID:  "nl-ams-1",
		JoinLink:  "call-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("second work session reused first id")
	}
	if len(removed) != 1 || removed[0].ID != first.ID {
		t.Fatalf("removed = %+v, want first session", removed)
	}
	if manager.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", manager.Count())
	}
}

func TestManagerCleanupExpired(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	manager := NewManager(Config{WorkTTL: time.Minute})
	manager.SetClockForTest(func() time.Time { return now })
	session, _, err := manager.CreateOrReplace(CreateSessionInput{
		UserID:    "user-1",
		RequestID: "req-1",
		Kind:      SessionKindWork,
		EgressID:  "de-fra-1",
		JoinLink:  "call-a",
	})
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute)
	removed := manager.CleanupExpired()
	if len(removed) != 1 || removed[0].ID != session.ID {
		t.Fatalf("removed = %+v, want session %s", removed, session.ID)
	}
	if manager.Count() != 0 {
		t.Fatalf("Count() = %d, want 0", manager.Count())
	}
}

func TestManagerEnforcesMaxUsers(t *testing.T) {
	manager := NewManager(Config{MaxUsers: 1})
	_, _, err := manager.CreateOrReplace(CreateSessionInput{
		UserID:    "user-1",
		RequestID: "req-1",
		Kind:      SessionKindService,
		JoinLink:  "service-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = manager.CreateOrReplace(CreateSessionInput{
		UserID:    "user-2",
		RequestID: "req-2",
		Kind:      SessionKindService,
		JoinLink:  "service-b",
	})
	if err == nil {
		t.Fatal("CreateOrReplace() expected max users error")
	}
}
