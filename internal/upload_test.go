package internal

import (
	"errors"
	"os"
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

func TestIsMoovAtEnd(t *testing.T) {
	// 创建临时文件写入测试 box
	writeMockMP4 := func(t *testing.T, ext string, boxes []struct {
		boxType string
		size    int64
	}) string {
		tmpFile, err := os.CreateTemp("", "test_*"+ext)
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}
		defer tmpFile.Close()

		for _, box := range boxes {
			if box.size == 1 {
				// largesize
				var sizeBuf [4]byte
				sizeBuf[3] = 1
				_, _ = tmpFile.Write(sizeBuf[:])
				_, _ = tmpFile.Write([]byte(box.boxType))
				// 假设 box 数据的总大小是 16（header 8 + large 8） + payload
				// 我们需要写入真正的 8 字节大小
				// 这里为了简单，我们写入 largesize，总大小写为 16 + 10（比如 26）
				var largeSizeBuf [8]byte
				// 写入 largeSize
				largeSize := uint64(16 + 10)
				largeSizeBuf[0] = byte(largeSize >> 56)
				largeSizeBuf[1] = byte(largeSize >> 48)
				largeSizeBuf[2] = byte(largeSize >> 40)
				largeSizeBuf[3] = byte(largeSize >> 32)
				largeSizeBuf[4] = byte(largeSize >> 24)
				largeSizeBuf[5] = byte(largeSize >> 16)
				largeSizeBuf[6] = byte(largeSize >> 8)
				largeSizeBuf[7] = byte(largeSize)
				_, _ = tmpFile.Write(largeSizeBuf[:])
				// 写入 10 字节 payload
				_, _ = tmpFile.Write(make([]byte, 10))
			} else {
				// normal size
				var sizeBuf [4]byte
				sizeBuf[0] = byte(box.size >> 24)
				sizeBuf[1] = byte(box.size >> 16)
				sizeBuf[2] = byte(box.size >> 8)
				sizeBuf[3] = byte(box.size)
				_, _ = tmpFile.Write(sizeBuf[:])
				_, _ = tmpFile.Write([]byte(box.boxType))
				// 写入 payload (size - 8 字节)
				if box.size > 8 {
					_, _ = tmpFile.Write(make([]byte, box.size-8))
				}
			}
		}
		return tmpFile.Name()
	}

	// 测试用例 1: 扩展名不是 mp4/mov
	txtFile := writeMockMP4(t, ".txt", nil)
	defer os.Remove(txtFile)
	atEnd, err := isMoovAtEnd(txtFile)
	if err != nil || atEnd {
		t.Errorf("expected false, nil for txt file, got %v, %v", atEnd, err)
	}

	// 测试用例 2: 文件不存在，应该报错
	_, err = isMoovAtEnd("non_existent_file.mp4")
	if err == nil {
		t.Errorf("expected error for non existent file, got nil")
	}

	// 测试用例 3: moov 在 mdat 之前 (正常的 faststart 视频)
	normalMP4 := writeMockMP4(t, ".mp4", []struct {
		boxType string
		size    int64
	}{
		{"ftyp", 16},
		{"moov", 24},
		{"mdat", 100},
	})
	defer os.Remove(normalMP4)
	atEnd, err = isMoovAtEnd(normalMP4)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if atEnd {
		t.Errorf("expected moov NOT at end (false), got true")
	}

	// 测试用例 4: mdat 在 moov 之前 (需要优化的视频)
	notFaststartMP4 := writeMockMP4(t, ".mp4", []struct {
		boxType string
		size    int64
	}{
		{"ftyp", 16},
		{"mdat", 100},
		{"moov", 24},
	})
	defer os.Remove(notFaststartMP4)
	atEnd, err = isMoovAtEnd(notFaststartMP4)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !atEnd {
		t.Errorf("expected moov at end (true), got false")
	}

	// 测试用例 5: largesize 且 mdat 在前面的情况
	largeMP4 := writeMockMP4(t, ".mp4", []struct {
		boxType string
		size    int64
	}{
		{"ftyp", 16},
		{"mdat", 1}, // size = 1, 代表使用 64位 largesize
		{"moov", 24},
	})
	defer os.Remove(largeMP4)
	atEnd, err = isMoovAtEnd(largeMP4)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !atEnd {
		t.Errorf("expected moov at end (true) for largesize, got false")
	}
}
