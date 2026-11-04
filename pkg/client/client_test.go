package client

import (
	"context"
	"errors"
	"testing"

	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

func TestStubClient(t *testing.T) {
	c := New(Options{})
	if err := c.Connect(context.Background(), "ws://localhost:7880", "t"); !errors.Is(err, errs.ErrUnimplemented) {
		t.Fatalf("got %v", err)
	}
}
