package internal

import (
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"  // 注册 GIF 解码器
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FFProbeResult 用于接收 ffprobe 的 JSON 输出
type FFProbeResult struct {
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		BitRate      string `json:"bit_rate"`
		Duration     string `json:"duration"`
		SideDataList []struct {
			SideDataType string `json:"side_data_type"`
			Rotation     int    `json:"rotation"`
		} `json:"side_data_list"`
		Tags struct {
			Rotate string `json:"rotate"`
		} `json:"tags"`
	} `json:"streams"`
	Format struct {
		BitRate  string `json:"bit_rate"`
		Duration string `json:"duration"`
	} `json:"format"`
}

// CheckMediaMain 入口：判断传入的是文件还是目录，支持递归模式
func CheckMediaMain(targetPath string, recursive bool) error {
	cleanPath := filepath.Clean(targetPath)
	fileInfo, err := os.Stat(cleanPath)
	if err != nil {
		return fmt.Errorf("路径不存在或无法访问: %w", err)
	}

	if fileInfo.IsDir() {
		if recursive {
			fmt.Printf("📂 开始扫描媒体目录: %s (深度递归检查子目录, 最大 3 层)\n\n", cleanPath)
			return scanAndCheckDir(cleanPath, 1)
		} else {
			fmt.Printf("📂 开始扫描媒体目录: %s (不检查子目录)\n\n", cleanPath)
			entries, err := os.ReadDir(cleanPath)
			if err != nil {
				return fmt.Errorf("读取目录失败: %w", err)
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue // 不检查子目录
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				fullPath := filepath.Join(cleanPath, entry.Name())
				analyzeFile(fullPath, info)
				fmt.Println(strings.Repeat("-", 40))
			}
		}
	} else {
		fmt.Printf("📄 开始扫描单文件: %s\n\n", cleanPath)
		analyzeFile(cleanPath, fileInfo)
	}
	return nil
}

// 递归扫描并检测媒体，最大深度限制为 3 层
func scanAndCheckDir(dirPath string, level int) error {
	if level > 3 {
		return nil
	}
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fullPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			if !strings.HasPrefix(entry.Name(), ".") {
				if err := scanAndCheckDir(fullPath, level+1); err != nil {
					return err
				}
			}
		} else {
			nameLower := strings.ToLower(entry.Name())
			if !strings.HasPrefix(nameLower, ".") && isMediaFile(entry.Name()) {
				info, err := entry.Info()
				if err == nil {
					analyzeFile(fullPath, info)
					fmt.Println(strings.Repeat("-", 40))
				}
			}
		}
	}
	return nil
}

// analyzeFile 核心解析函数
func analyzeFile(path string, info fs.FileInfo) {
	ext := strings.ToLower(filepath.Ext(path))
	fmt.Printf("📝 文件名: %s\n", info.Name())
	fmt.Printf("   ├─ 格式/后缀: %s\n", ext)
	fmt.Printf("   ├─ 大小: %.2f KB\n", float64(info.Size())/1024)
	fmt.Printf("   ├─ 修改时间: %s\n", info.ModTime().Format("2006-01-02 15:04:05"))

	// 根据后缀初步分流（图片 vs 视频）
	if isImage(ext) {
		analyzeImage(path)
	} else if isVideo(ext) {
		analyzeVideo(path)
	} else {
		fmt.Println("   └─ 提示: 非支持的图片或视频媒体格式，跳过深度解析。")
	}
}

func isImage(ext string) bool {
	return ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp"
}

func isVideo(ext string) bool {
	return ext == ".mp4" || ext == ".mkv" || ext == ".avi" || ext == ".mov" || ext == ".flv"
}

// 1. 图片深度解析 (使用 Go 原生 image 库)
func analyzeImage(path string) {
	file, err := os.Open(path)
	if err != nil {
		fmt.Printf("   └─ ❌ 无法打开图片进行深度解析: %v\n", err)
		return
	}
	defer file.Close()

	// image.DecodeConfig 可以直接获取宽、高和格式，不需要把整张图读入内存，速度极快
	config, formatName, err := image.DecodeConfig(file)
	if err != nil {
		fmt.Printf("   └─ ❌ 图片解码失败 (可能非标准图片扩展名): %v\n", err)
		return
	}

	fmt.Printf("   ├─ 图片编码: %s\n", formatName)
	fmt.Printf("   ├─ 分辨率(宽高): %d x %d\n", config.Width, config.Height)
	fmt.Printf("   └─ 总像素量: %.1f 万像素\n", float64(config.Width*config.Height)/10000)
}

// 2. 视频深度解析 (通过 os/exec 调用系统的 ffprobe)
func analyzeVideo(path string) {
	// 组装 ffprobe 命令，让其返回标准的 JSON 字符串
	cmd := exec.Command("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path)
	output, err := cmd.Output()
	if err != nil {
		fmt.Println("   └─ ❌ 视频解析失败: 请检查系统是否安装了 ffmpeg/ffprobe")
		return
	}

	var data FFProbeResult
	if err := json.Unmarshal(output, &data); err != nil {
		fmt.Printf("   └─ ❌ 解析元数据 JSON 失败: %v\n", err)
		return
	}

	// 解析格式层面的总时长 and 总码率
	var totalDuration, totalBitrate string
	if data.Format.Duration != "" {
		d, _ := time.ParseDuration(data.Format.Duration + "s")
		totalDuration = d.Round(time.Second).String()
	}
	if data.Format.BitRate != "" {
		var br int64
		fmt.Sscanf(data.Format.BitRate, "%d", &br)
		totalBitrate = fmt.Sprintf("%d Kbps", br/1000)
	}

	fmt.Printf("   ├─ 总时长: %s\n", totalDuration)
	fmt.Printf("   ├─ 总码率: %s\n", totalBitrate)

	// 遍历流（Stream）提取视频流和音频流信息
	for _, stream := range data.Streams {
		if stream.CodecType == "video" {
			var vBitrate = "未知"
			if stream.BitRate != "" {
				var br int64
				fmt.Sscanf(stream.BitRate, "%d", &br)
				vBitrate = fmt.Sprintf("%d Kbps", br/1000)
			}
			fmt.Printf("   ├─ [视频流] 编码格式: %s\n", stream.CodecName)
			fmt.Printf("   ├─ [视频流] 分辨率(宽高): %d x %d\n", stream.Width, stream.Height)

			// 检查视频流的旋转角度
			rotation := 0
			for _, sd := range stream.SideDataList {
				if strings.EqualFold(sd.SideDataType, "Display Matrix") {
					rotation = sd.Rotation
				}
			}
			if rotation == 0 && stream.Tags.Rotate != "" {
				var rot int
				if _, err := fmt.Sscanf(stream.Tags.Rotate, "%d", &rot); err == nil {
					rotation = rot
				}
			}
			if rotation != 0 {
				fmt.Printf("   ├─ [视频流] 旋转角度: %d 度\n", rotation)
				absRot := rotation
				if absRot < 0 {
					absRot = -absRot
				}
				if absRot == 90 || absRot == 270 {
					fmt.Printf("   ├─ [视频流] 实际播放分辨率: %d x %d\n", stream.Height, stream.Width)
				}
			}

			fmt.Printf("   ├─ [视频流] 视频码率: %s\n", vBitrate)
		} else if stream.CodecType == "audio" {
			var aBitrate = "未知"
			if stream.BitRate != "" {
				var br int64
				fmt.Sscanf(stream.BitRate, "%d", &br)
				aBitrate = fmt.Sprintf("%d Kbps", br/1000)
			}
			fmt.Printf("   ├─ [音频流] 编码格式: %s\n", stream.CodecName)
			fmt.Printf("   └─ [音频流] 音频码率: %s\n", aBitrate)
		}
	}
}
