package errors_test

import (
	"fmt"
	"testing"

	"github.com/opensraph/srpc/errors"
)

const errorFormat = "error: code = %d desc = %s"

func TestNewEqual(t *testing.T) {
	// Different allocations should not be equal.
	if errors.New("abc") == errors.New("abc") {
		t.Errorf(`New("abc") == New("abc")`)
	}
	if errors.New("abc") == errors.New("xyz") {
		t.Errorf(`New("abc") == New("xyz")`)
	}

	// Same allocation should be equal to itself (not crash).
	err := errors.New("jkl")
	if err != err {
		t.Errorf(`err != err`)
	}
}

func TestErrorMethod(t *testing.T) {
	err := errors.New("abc")
	want := fmt.Sprintf(errorFormat, errors.Unknown, "abc")
	if err.Error() != want {
		t.Errorf(`New("abc").Error() = %q, want %q`, err.Error(), want)
	}
}
