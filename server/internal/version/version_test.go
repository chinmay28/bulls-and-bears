package version

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"
)

func TestStringIsUnstampedByDefault(t *testing.T) {
	// The zero-configuration build — `go test`, or a bare `go build` — must be
	// visibly a development build rather than claim to be patch release 1.
	if Patch != "0" {
		t.Fatalf("Patch = %q, want the unstamped default", Patch)
	}
	if got, want := String(), fmt.Sprintf("v%d.%d.0", Year, Month); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if Stamped() {
		t.Error("Stamped() = true on an unstamped build")
	}
}

func TestStringUsesTheStampedPatch(t *testing.T) {
	t.Cleanup(func() { Patch = "0" })
	Patch = "42"
	if got, want := String(), fmt.Sprintf("v%d.%d.42", Year, Month); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if !Stamped() {
		t.Error("Stamped() = false with a patch number stamped in")
	}
}

// scripts/version.mjs reads Year and Month out of this package's source so
// that the PWA and the binary can't disagree about them, which makes the *shape*
// of those two lines part of the contract. This is the test that notices when a
// reformat — a shared const line, a comment on the end — breaks the script.
func TestYearMonthStayReadableToTheBuildScript(t *testing.T) {
	src, err := os.ReadFile("version.go")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"Year": Year, "Month": Month} {
		// The same pattern scripts/version.mjs applies.
		re := regexp.MustCompile(`(?m)^[ \t]*` + name + `[ \t]*=[ \t]*(\d+)[ \t]*$`)
		m := re.FindSubmatch(src)
		if m == nil {
			t.Errorf("scripts/version.mjs could not find %s in version.go", name)
			continue
		}
		if got, _ := strconv.Atoi(string(m[1])); got != want {
			t.Errorf("%s reads as %d, but the constant is %d", name, got, want)
		}
	}
}

// The month is part of a version string that has to stay valid semver and has
// to name a real month — scripts/version.mjs refuses to assemble a version out
// of anything else, so a typo here should fail here rather than at build time.
func TestMonthIsACalendarMonth(t *testing.T) {
	if Month < 1 || Month > 12 {
		t.Errorf("Month = %d, want a calendar month (1-12)", Month)
	}
}
