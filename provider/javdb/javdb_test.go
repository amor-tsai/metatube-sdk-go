package javdb

import (
	"testing"

	"github.com/metatube-community/metatube-sdk-go/provider/internal/testkit"
)

func TestJavDB_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"rYD7J", // MIUM-526
		"ZY5eq",
	})
}

func TestJavDB_SearchMovie(t *testing.T) {
	testkit.Test(t, New, []string{
		"MIUM-526",
		"LUXU-1366",
	})
}

func TestJavDB_SearchMovie_SkipsLoginOnly(t *testing.T) {
	// LAFBD is uncensored but not caught by number.IsSpecial; its page
	// requires login, so without a cookie nothing should be offered.
	results, err := New().SearchMovie("LAFBD-41")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results without a cookie, got %d", len(results))
	}
}

func TestIsSameNumber(t *testing.T) {
	for _, tc := range []struct {
		number, keyword string
		want            bool
	}{
		{"MIUM-526", "MIUM-526", true},
		{"MIUM-526", "300MIUM-526", true},
		{"LAFBD-41", "lafbd_41", true},
		{"MIUM-546", "MIUM-526", false},
		{"", "MIUM-526", false},
	} {
		if got := isSameNumber(tc.number, tc.keyword); got != tc.want {
			t.Errorf("isSameNumber(%q, %q) = %v, want %v", tc.number, tc.keyword, got, tc.want)
		}
	}
}
