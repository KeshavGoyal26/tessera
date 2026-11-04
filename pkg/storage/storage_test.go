package storage

import (
	"context"
	"testing"
)

func TestFromOptionsDefault(t *testing.T) {
	s, err := FromOptions(context.Background(), nil, nil)
	if err != nil || s != nil {
		t.Fatalf("nil options must return default: %v %v", s, err)
	}
}
