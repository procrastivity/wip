package registry

import (
	"strings"
	"testing"
)

// TestNamesOfPanicsOnDuplicate proves C4.2's registration panic: two rows
// naming the same harness must stop the program at Names's initializer,
// not silently collapse into one entry that install, uninstall, and doctor
// would then read inconsistently.
func TestNamesOfPanicsOnDuplicate(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("namesOf did not panic on a duplicate name")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value = %v (%T), want a string naming the duplicate", r, r)
		}
		if !strings.Contains(msg, "same-name") {
			t.Errorf("panic message %q does not name the duplicate", msg)
		}
	}()

	namesOf([]Harness{{Name: "same-name"}, {Name: "same-name"}})
}
