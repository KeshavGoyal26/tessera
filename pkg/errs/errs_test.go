package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsMatchesByCode(t *testing.T) {
	err := New(CodeNotFound, "room %q not found", "a")
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("expected not_found class")
	}
	if errors.Is(err, ErrInternal) {
		t.Fatal("unexpected internal class")
	}
	wrapped := fmt.Errorf("lookup: %w", err)
	if !errors.Is(wrapped, ErrNotFound) || CodeOf(wrapped) != CodeNotFound {
		t.Fatal("class must survive wrapping")
	}
}

func TestCodeOf(t *testing.T) {
	if CodeOf(nil) != "" {
		t.Fatal("nil must have empty code")
	}
	if CodeOf(errors.New("x")) != CodeInternal {
		t.Fatal("foreign errors are internal")
	}
	if !errors.Is(NotImplemented("rtc.Engine"), ErrUnimplemented) {
		t.Fatal("NotImplemented must be unimplemented")
	}
}

func TestHTTPStatus(t *testing.T) {
	cases := map[Code]int{
		CodeInvalidArgument: 400, CodeUnauthenticated: 401, CodePermissionDenied: 403,
		CodeNotFound: 404, CodeAlreadyExists: 409, CodeResourceExhausted: 429,
		CodeUnimplemented: 501, CodeUnavailable: 503, CodeInternal: 500,
	}
	for c, want := range cases {
		if got := HTTPStatus(c); got != want {
			t.Errorf("%s: got %d want %d", c, got, want)
		}
	}
}
