package notify

import "testing"

func TestRoomWildcardRejected(t *testing.T) {
	cases := []struct {
		topic string
		want  bool
	}{
		{"room:ZoOjbGtmjxSXq1w2e3r4t5y6", true},
		{"room:*", false},
		{"room:Zo*", false},
		{"room:*mjxSX", false},
		{"models", false},
		{"room:", false},
	}
	for _, c := range cases {
		if got := ValidRoomTopic(c.topic); got != c.want {
			t.Errorf("ValidRoomTopic(%q) = %v, want %v", c.topic, got, c.want)
		}
	}
}

func TestRejectRoomWildcardsKeepsNonRoom(t *testing.T) {
	in := []string{"models", "room:*", "room:AbCdEfGhIjKlMnOpQrStUvWx", "prices"}
	out := rejectRoomWildcards(in)
	want := map[string]bool{"models": true, "room:AbCdEfGhIjKlMnOpQrStUvWx": true, "prices": true}
	if len(out) != 3 {
		t.Fatalf("got %v, want 3 entries", out)
	}
	for _, t2 := range out {
		if !want[t2] {
			t.Errorf("unexpected topic survived: %q", t2)
		}
	}
}
