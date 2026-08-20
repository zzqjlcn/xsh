package credential

import (
	"errors"
	"testing"
)

func TestNormalizeUnavailableError(t *testing.T) {
	err := errors.New("The name org.freedesktop.secrets was not provided by any .service files")
	if got := normalizeError(err); !errors.Is(got, ErrUnavailable) {
		t.Fatalf("normalizeError() = %v", got)
	}
}

func TestNormalizeOtherError(t *testing.T) {
	want := errors.New("permission denied")
	if got := normalizeError(want); !errors.Is(got, want) {
		t.Fatalf("normalizeError() = %v", got)
	}
}
