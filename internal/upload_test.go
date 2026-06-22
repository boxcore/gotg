package internal

import (
	"errors"
	"testing"
)

func TestIsWebDAVIOError(t *testing.T) {
	tests := []struct {
		err      error
		expected bool
	}{
		{errors.New("connection timed out"), true},
		{errors.New("operation not supported"), true},
		{errors.New("read: input/output error"), true},
		{errors.New("transport endpoint is not connected"), true},
		{errors.New("connection reset by peer"), true},
		{errors.New("broken pipe"), true},
		{errors.New("some regular error"), false},
		{nil, false},
	}

	for _, tt := range tests {
		result := isWebDAVIOError(tt.err)
		if result != tt.expected {
			t.Errorf("isWebDAVIOError(%v) = %v; want %v", tt.err, result, tt.expected)
		}
	}
}
