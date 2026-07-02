package main

import "testing"

func TestParseServiceUserIDs(t *testing.T) {
	users := parseServiceUserIDs(" user-1, user-2,user-1,, ")
	if len(users) != 2 {
		t.Fatalf("expected 2 unique users, got %d", len(users))
	}
	for _, userID := range []string{"user-1", "user-2"} {
		if _, ok := users[userID]; !ok {
			t.Fatalf("missing user %q", userID)
		}
	}
}
