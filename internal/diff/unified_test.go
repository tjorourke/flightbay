package diff

import (
	"fmt"
	"strings"
	"testing"
)

func TestUnified(t *testing.T) {
	if Unified("a\n", "a\n", "x", "y", 3) != "" {
		t.Fatal("equal inputs gave a diff")
	}
	var a, b []string
	for i := 1; i <= 20; i++ {
		a = append(a, fmt.Sprint(i))
		b = append(b, fmt.Sprint(i))
	}
	b[1] = "two"
	b[17] = "eighteen"
	got := Unified(strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n", "old", "new", 3)
	want := "--- old\n+++ new\n" +
		"@@ -1,5 +1,5 @@\n 1\n-2\n+two\n 3\n 4\n 5\n" +
		"@@ -15,6 +15,6 @@\n 15\n 16\n 17\n-18\n+eighteen\n 19\n 20\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if got := Unified("", "x\n", "old", "new", 3); got != "--- old\n+++ new\n@@ -0,0 +1 @@\n+x\n" {
		t.Fatalf("from empty:\n%s", got)
	}
}
