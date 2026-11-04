package client

import (
	"context"
	"testing"
)

func TestConnectFailsWithoutServer(t *testing.T) {
	c := New(Options{})
	if err := c.Connect(context.Background(), "ws://127.0.0.1:1", "t"); err == nil {
		t.Fatal("expected error")
	}
}
