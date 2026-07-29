package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func isMoovAtEnd(filePath string) (bool, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != ".mp4" && ext != ".mov" {
		return false, nil
	}

	// 使用 ffprobe -v trace 扫描所有的 box 信息 (输出在 stderr 中)
	cmd := exec.Command("ffprobe", "-v", "trace", "-i", filePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// 运行命令 (不需要关心 exit code，因为 ffprobe trace 正常情况下会输出信息，即使退出码非 0 也没事)
	_ = cmd.Run()

	output := stderr.String()

	// 查找 type:'mdat' 和 type:'moov' 在 trace 输出中的相对位置
	// 注意 ffprobe 输出格式形如: [mp4 @ 0x...] type:'ftyp' ...
	// 或者是 type:'mdat' 和 type:'moov'
	mdatIdx := strings.Index(output, "type:'mdat'")
	moovIdx := strings.Index(output, "type:'moov'")

	fmt.Printf("[DEBUG] mdatIdx: %d, moovIdx: %d\n", mdatIdx, moovIdx)

	if mdatIdx == -1 || moovIdx == -1 {
		// 如果其中任何一个没找到，默认不进行转码
		return false, nil
	}

	// 如果 mdat 在 moov 之前，说明 moov 在后面，返回 true
	return mdatIdx < moovIdx, nil
}

func main() {
	fmt.Println("--- Testing test_normal.mp4 ---")
	atEnd, err := isMoovAtEnd("test_normal.mp4")
	fmt.Printf("Result for test_normal.mp4: atEnd=%v, err=%v\n\n", atEnd, err)

	fmt.Println("--- Testing test_fast.mp4 ---")
	atEnd, err = isMoovAtEnd("test_fast.mp4")
	fmt.Printf("Result for test_fast.mp4: atEnd=%v, err=%v\n\n", atEnd, err)

	fmt.Println("--- Testing test_fast_audio.mp4 ---")
	atEnd, err = isMoovAtEnd("test_fast_audio.mp4")
	fmt.Printf("Result for test_fast_audio.mp4: atEnd=%v, err=%v\n", atEnd, err)
}
