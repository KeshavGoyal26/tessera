package room

import (
	"context"
	"errors"
	"testing"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

func TestStubManager(t *testing.T) {
	m, err := NewManager(Params{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ListRooms(context.Background(), nil); !errors.Is(err, errs.ErrUnimplemented) {
		t.Fatalf("got %v", err)
	}
}
