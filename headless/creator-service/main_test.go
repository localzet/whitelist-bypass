package main

import "testing"

func TestParseAllowedUsers(t *testing.T) {
	got := parseAllowedUsers("legacy-user", " user-1,user-2,user-1, ")
	for _, userID := range []string{"legacy-user", "user-1", "user-2"} {
		if _, ok := got[userID]; !ok {
			t.Fatalf("missing user %q", userID)
		}
	}
	if len(got) != 3 {
		t.Fatalf("got %d users, want 3", len(got))
	}
}
