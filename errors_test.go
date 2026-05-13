package dalgo2sqlite

import (
	"strings"
	"testing"
)

func TestErrCollectionNotFound_Message(t *testing.T) {
	t.Parallel()
	err := newCollectionNotFoundError("users")
	msg := err.Error()
	if !strings.Contains(msg, "not found") {
		t.Errorf("expected error message to contain 'not found'; got: %s", msg)
	}
	if !strings.Contains(msg, "users") {
		t.Errorf("expected error message to contain collection name 'users'; got: %s", msg)
	}
}
