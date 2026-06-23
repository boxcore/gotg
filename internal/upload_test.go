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
		{errors.New("ls: reading directory '.': state not recoverable"), true},
		{errors.New("telego: sendMediaGroup: api: 400 \"Bad Request: failed to send message #1\""), false},
		{errors.New("too many requests: retry after 8"), false},
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

func TestSplitIntoThree(t *testing.T) {
	tests := []struct {
		name          string
		batchSize     int
		expectedSizes []int
	}{
		{"empty", 0, nil},
		{"single file", 1, []int{1}},
		{"two files", 2, []int{1, 1}},
		{"three files", 3, []int{1, 1, 1}},
		{"four files", 4, []int{2, 1, 1}},
		{"five files", 5, []int{2, 2, 1}},
		{"six files", 6, []int{2, 2, 2}},
		{"nine files", 9, []int{3, 3, 3}},
		{"ten files", 10, []int{4, 3, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var batch []uploadFile
			for i := 0; i < tt.batchSize; i++ {
				batch = append(batch, uploadFile{})
			}
			res := splitIntoThree(batch)
			if len(res) != len(tt.expectedSizes) {
				t.Fatalf("expected %d parts, got %d", len(tt.expectedSizes), len(res))
			}
			for idx, size := range tt.expectedSizes {
				if len(res[idx]) != size {
					t.Errorf("part %d: expected size %d, got %d", idx, size, len(res[idx]))
				}
			}
		})
	}
}
