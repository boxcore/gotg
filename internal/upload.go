package internal

import (
	"context" // 💡 引入 context 包
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"  // 注册 GIF 解码器
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	"github.com/rwcarlsen/goexif/exif"
)

type MediaMetadata struct {
	SHA1        string    `json:"sha1"`
	FileName    string    `json:"file_name"`
	FilePath    string    `json:"file_path"`
	FileSize    int64     `json:"file_size"`
	ModTime     time.Time `json:"mod_time"`
	CreatedTime time.Time `json:"created_time"`
	FileType    string    `json:"file_type"` // "video", "image", "other"

	// 视频特有
	Duration     float64 `json:"duration,omitempty"` // 秒
	Width        int     `json:"width,omitempty"`
	Height       int     `json:"height,omitempty"`
	AspectRatio  string  `json:"aspect_ratio,omitempty"`
	VideoCodec   string  `json:"video_codec,omitempty"`
	VideoBitrate string  `json:"video_bitrate,omitempty"`
	AudioCodec   string  `json:"audio_codec,omitempty"`
	AudioBitrate string  `json:"audio_bitrate,omitempty"`

	// 图片特有
	ImageCodec string `json:"image_codec,omitempty"`
	Resolution string `json:"resolution,omitempty"` // "WidthxHeight"
	GeoInfo    string `json:"geo_info,omitempty"`

	// 缓存生成文件
	ThumbPath string `json:"thumb_path,omitempty"` // 缩略图本地缓存路径
	TranPath  string `json:"tran_path,omitempty"`  // 转码/压缩后图片本地缓存路径
}

type uploadFile struct {
	path        string
	name        string
	size        int64
	modTime     time.Time
	createdTime time.Time
	metadata    MediaMetadata
}

// UploadDirectoryFiles 读取 targetPath 下的一级文件并上传到指定频道
func UploadDirectoryFiles(token string, chatIDStr string, targetPath string, apiURL string, groupSize int, debugMode string, sortType string, cacheDir string, cacheFresh bool, sleepTime int) error {
	cleanPath := filepath.Clean(targetPath)
	fileInfo, err := os.Stat(cleanPath)
	if err != nil {
		return fmt.Errorf("路径不存在或无法访问: %w", err)
	}

	if !fileInfo.IsDir() {
		return fmt.Errorf("指定的路径 '%s' 不是一个目录", targetPath)
	}

	entries, err := os.ReadDir(cleanPath)
	if err != nil {
		return fmt.Errorf("读取目录失败: %w", err)
	}

	var filesToUpload []uploadFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			fmt.Printf("❌ 获取文件信息失败 (%s): %v\n", entry.Name(), err)
			continue
		}

		// 💡 慢速遍历控制：每次扫描完一个文件休眠 100 毫秒，防止 WebDAV 密集 API 限制/风控
		time.Sleep(100 * time.Millisecond)

		filePath := filepath.Join(cleanPath, entry.Name())
		// 处理媒体文件缓存与智能处理逻辑
		meta, err := processMedia(filePath, info, cacheDir, cacheFresh)
		if err != nil {
			fmt.Printf("⚠️  媒体预处理失败 (%s): %v，将按普通文件处理\n", entry.Name(), err)
			meta = MediaMetadata{
				FileName:    entry.Name(),
				FilePath:    filePath,
				FileSize:    info.Size(),
				ModTime:     info.ModTime(),
				CreatedTime: getBirthTime(info),
				FileType:    "other",
			}
		}

		// 💡 在网络层面限制：仍然基于上传时的限制 (对于 official API 是 50MB，自定义 API 2GB)
		// 如果生成了 _tran 压缩文件，用它的大小来进行拦截校验
		realUploadSize := meta.FileSize
		if meta.TranPath != "" {
			if tranInfo, err := os.Stat(meta.TranPath); err == nil {
				realUploadSize = tranInfo.Size()
			}
		}

		limit := int64(50 * 1024 * 1024) // 50MB 官方限制
		limitStr := "50MB"
		if apiURL != "" {
			limit = 2000 * 1024 * 1024 // 2GB 自定义限制
			limitStr = "2GB"
		}

		if realUploadSize > limit {
			fmt.Printf("❌ 跳过 %s (上传体积 %s 超过 %s 限制)\n", entry.Name(), formatSize(realUploadSize), limitStr)
			continue
		}

		filesToUpload = append(filesToUpload, uploadFile{
			path:        filePath,
			name:        entry.Name(),
			size:        realUploadSize,
			modTime:     meta.ModTime,
			createdTime: meta.CreatedTime,
			metadata:    meta,
		})
	}

	// 根据指定的 sortType 对文件进行排序
	sort.Slice(filesToUpload, func(i, j int) bool {
		f1, f2 := filesToUpload[i], filesToUpload[j]
		switch sortType {
		case "name_asc":
			return naturalLess(f1.name, f2.name)
		case "name_desc":
			return naturalLess(f2.name, f1.name)
		case "size_asc":
			return f1.size < f2.size
		case "size_desc":
			return f1.size > f2.size
		case "mod_asc":
			return f1.modTime.Before(f2.modTime)
		case "mod_desc":
			return f1.modTime.After(f2.modTime)
		case "created_asc":
			return f1.createdTime.Before(f2.createdTime)
		case "created_desc":
			return f1.createdTime.After(f2.createdTime)
		default:
			return naturalLess(f1.name, f2.name)
		}
	})

	// 💡 调试模式：仅打印分组及上传顺序，不执行上传
	if debugMode == "list" {
		fmt.Printf("📂 [DEBUG 模式] 扫描目录: %s (共找到 %d 个待上传文件, 分组大小: %d)\n", cleanPath, len(filesToUpload), groupSize)
		if len(filesToUpload) == 0 {
			fmt.Println("没有符合上传条件的文件。")
			return nil
		}

		totalBatches := (len(filesToUpload) + groupSize - 1) / groupSize
		for i := 0; i < len(filesToUpload); i += groupSize {
			end := i + groupSize
			if end > len(filesToUpload) {
				end = len(filesToUpload)
			}
			batchNum := i/groupSize + 1
			batchFiles := filesToUpload[i:end]
			fmt.Printf("\n📦 [组 %d/%d] (文件数: %d)\n", batchNum, totalBatches, len(batchFiles))
			for idx, f := range batchFiles {
				details := ""
				if f.metadata.FileType == "video" {
					details = fmt.Sprintf("时长: %.1fs, 比例: %s", f.metadata.Duration, f.metadata.AspectRatio)
				} else if f.metadata.FileType == "image" {
					details = fmt.Sprintf("分辨率: %s", f.metadata.Resolution)
					if f.metadata.GeoInfo != "" {
						details += fmt.Sprintf(", 地理信息: [%s]", f.metadata.GeoInfo)
					}
				}
				if f.metadata.TranPath != "" {
					details += ", [已自适应转码压缩]"
				}
				if details != "" {
					details = " (" + details + ")"
				}
				fmt.Printf("  ├─ %d. %s (%s)%s\n", idx+1, f.name, formatSize(f.size), details)
			}
		}
		return nil
	}

	// 💡 调试模式：打印 curl 模拟命令，不执行实际上传
	if debugMode == "curl" {
		fmt.Printf("📂 [DEBUG CURL 模式] 扫描目录: %s (共找到 %d 个待上传文件, 分组大小: %d)\n", cleanPath, len(filesToUpload), groupSize)
		if len(filesToUpload) == 0 {
			fmt.Println("没有符合上传条件的文件。")
			return nil
		}

		baseAPI := "https://api.telegram.org"
		if apiURL != "" {
			baseAPI = strings.TrimSuffix(apiURL, "/")
		}
		apiEndpoint := fmt.Sprintf("%s/bot%s/sendMediaGroup", baseAPI, token)

		totalBatches := (len(filesToUpload) + groupSize - 1) / groupSize
		for i := 0; i < len(filesToUpload); i += groupSize {
			end := i + groupSize
			if end > len(filesToUpload) {
				end = len(filesToUpload)
			}
			batchNum := i/groupSize + 1
			batchFiles := filesToUpload[i:end]

			fmt.Printf("\n📦 [组 %d/%d] (文件数: %d) curl 命令模拟:\n", batchNum, totalBatches, len(batchFiles))

			var mediaItems []string
			var filesFields []string
			for idx, f := range batchFiles {
				attachName := fmt.Sprintf("file%d", idx)
				uploadPath := f.path
				if f.metadata.TranPath != "" {
					uploadPath = f.metadata.TranPath
				}

				if f.metadata.ThumbPath != "" {
					thumbAttachName := fmt.Sprintf("thumb%d", idx)
					mediaItems = append(mediaItems, fmt.Sprintf(`{"type":"document","media":"attach://%s","thumbnail":"attach://%s"}`, attachName, thumbAttachName))
					filesFields = append(filesFields, fmt.Sprintf(`  -F "%s=@%s"`, attachName, uploadPath))
					filesFields = append(filesFields, fmt.Sprintf(`  -F "%s=@%s"`, thumbAttachName, f.metadata.ThumbPath))
				} else {
					mediaItems = append(mediaItems, fmt.Sprintf(`{"type":"document","media":"attach://%s"}`, attachName))
					filesFields = append(filesFields, fmt.Sprintf(`  -F "%s=@%s"`, attachName, uploadPath))
				}
			}

			mediaJSON := fmt.Sprintf("[%s]", strings.Join(mediaItems, ","))

			fmt.Println("```bash")
			fmt.Printf("curl -s -X POST %q \\\n", apiEndpoint)
			fmt.Printf("  -F \"chat_id=%s\" \\\n", chatIDStr)
			fmt.Printf("  -F 'media=%s' \\\n", mediaJSON)
			fmt.Println(strings.Join(filesFields, " \\\n"))
			fmt.Println("```")

			if i+groupSize < len(filesToUpload) {
				fmt.Printf("💤 [CURL 模式] 每组输出后模拟休眠 %d 秒...\n", sleepTime)
			}
		}
		return nil
	}

	var opts []telego.BotOption
	opts = append(opts, telego.WithDefaultLogger(false, false))
	if apiURL != "" {
		opts = append(opts, telego.WithAPIServer(apiURL))
	}
	bot, err := telego.NewBot(token, opts...)
	if err != nil {
		return fmt.Errorf("初始化 Bot 失败: %w", err)
	}
	chatID := telego.ChatID{Username: chatIDStr}

	fmt.Printf("📂 开始扫描并上传目录: %s (共 %d 个待上传文件)\n", cleanPath, len(filesToUpload))

	count := 0
	totalFiles := len(filesToUpload)
	for i := 0; i < totalFiles; i += groupSize {
		end := i + groupSize
		if end > totalFiles {
			end = totalFiles
		}
		batch := filesToUpload[i:end]
		batchSize := len(batch)

		// 统一使用 SendMediaGroup 上传
		fmt.Printf("🚀 正在以媒体组上传 %d 个文件...\n", batchSize)
		var openFiles []*os.File
		var mediaList []telego.InputMedia
		var openErr error

		for _, f := range batch {
			uploadPath := f.path
			if f.metadata.TranPath != "" {
				uploadPath = f.metadata.TranPath
				fmt.Printf("  ⚠️  原图规格超限，已自动切换为转码图: %s\n", filepath.Base(uploadPath))
			}

			fmt.Printf("  ├─ 准备文件: %s (%s)\n", f.name, formatSize(f.size))
			fileHandle, err := os.Open(uploadPath)
			if err != nil {
				openErr = err
				fmt.Printf("  ❌ 打开文件失败: %v\n", err)
				break
			}
			openFiles = append(openFiles, fileHandle)

			doc := &telego.InputMediaDocument{
				Type:  "document",
				Media: telego.InputFile{File: fileHandle},
			}

			// 如果生成了缩略图，挂载它
			if f.metadata.ThumbPath != "" {
				thumbHandle, err := os.Open(f.metadata.ThumbPath)
				if err != nil {
					fmt.Printf("  ⚠️  打开缩略图失败: %v，将不携带缩略图上传\n", err)
				} else {
					openFiles = append(openFiles, thumbHandle)
					doc.Thumbnail = &telego.InputFile{File: thumbHandle}
				}
			}

			mediaList = append(mediaList, doc)
		}

		// 如果有任何文件打开失败，关闭这批已打开的文件，然后继续下一组
		if openErr != nil {
			for _, fh := range openFiles {
				_ = fh.Close()
			}
			fmt.Println("❌ 上传组失败: 无法打开部分文件")
			continue
		}

		// 发送媒体组
		_, err = bot.SendMediaGroup(context.Background(), &telego.SendMediaGroupParams{
			ChatID: chatID,
			Media:  mediaList,
		})

		// 无论成功还是失败，关闭所有已打开的文件
		for _, fh := range openFiles {
			_ = fh.Close()
		}

		if err != nil {
			fmt.Printf("❌ 媒体组上传失败: %v\n", err)
		} else {
			fmt.Printf("✅ 媒体组上传成功 (%d 个文件)\n", batchSize)
			count += batchSize
		}

		// 如果不是最后一组，执行休眠防限流
		if i+groupSize < totalFiles {
			fmt.Printf("💤 已完成该媒体组处理，根据设置休眠 %d 秒以防止 API 频控/风控...\n", sleepTime)
			time.Sleep(time.Duration(sleepTime) * time.Second)
		}
	}

	fmt.Printf("🎉 上传任务结束，成功上传了 %d 个文件。\n", count)
	return nil
}

// ---------------------- 核心分析与缓存处理函数 ----------------------

func calculateSHA1(name string, created, mod time.Time, size int64) string {
	h := sha1.New()
	h.Write([]byte(name))
	h.Write([]byte(created.Format(time.RFC3339)))
	h.Write([]byte(mod.Format(time.RFC3339)))
	h.Write([]byte(fmt.Sprintf("%d", size)))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func getGPSInfo(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return "", err
	}

	lat, lon, err := x.LatLong()
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Lat: %.6f, Lon: %.6f", lat, lon), nil
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func getAspectRatio(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	g := gcd(w, h)
	return fmt.Sprintf("%d:%d", w/g, h/g)
}

func processMedia(filePath string, info os.FileInfo, cacheDir string, cacheFresh bool) (MediaMetadata, error) {
	createdTime := getBirthTime(info)
	sha1Val := calculateSHA1(info.Name(), createdTime, info.ModTime(), info.Size())
	jsonPath := filepath.Join(cacheDir, sha1Val+".json")

	// 1. 如果缓存存在，且不要求刷新，直接读缓存
	if !cacheFresh {
		if cachedData, err := os.ReadFile(jsonPath); err == nil {
			var meta MediaMetadata
			if err := json.Unmarshal(cachedData, &meta); err == nil {
				// 校验物理文件及关联缓存文件是否都真实存在
				filesExist := true
				if _, err := os.Stat(meta.FilePath); err != nil {
					filesExist = false
				}
				if meta.ThumbPath != "" {
					if _, err := os.Stat(meta.ThumbPath); err != nil {
						filesExist = false
					}
				}
				if meta.TranPath != "" {
					if _, err := os.Stat(meta.TranPath); err != nil {
						filesExist = false
					}
				}

				if filesExist {
					return meta, nil
				}
			}
		}
	}

	// 2. 缓存不存在或需刷新，全新扫描
	ext := strings.ToLower(filepath.Ext(filePath))
	meta := MediaMetadata{
		SHA1:        sha1Val,
		FileName:    info.Name(),
		FilePath:    filePath,
		FileSize:    info.Size(),
		ModTime:     info.ModTime(),
		CreatedTime: createdTime,
		FileType:    "other",
	}

	isVideoFile := (ext == ".mp4" || ext == ".mov")
	isImageFile := (ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp")

	if isVideoFile {
		meta.FileType = "video"
		// A. ffprobe 解析视频元数据
		cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height,bit_rate,duration:format=duration,bit_rate", "-of", "json", filePath)
		output, err := cmd.Output()
		if err == nil {
			var data FFProbeResult
			if err := json.Unmarshal(output, &data); err == nil {
				for _, stream := range data.Streams {
					if stream.CodecType == "video" {
						meta.Width = stream.Width
						meta.Height = stream.Height
						meta.VideoCodec = stream.CodecName
						if stream.BitRate != "" {
							var br int64
							fmt.Sscanf(stream.BitRate, "%d", &br)
							meta.VideoBitrate = fmt.Sprintf("%d Kbps", br/1000)
						}
						if stream.Duration != "" {
							var dur float64
							fmt.Sscanf(stream.Duration, "%f", &dur)
							meta.Duration = dur
						}
					} else if stream.CodecType == "audio" {
						meta.AudioCodec = stream.CodecName
						if stream.BitRate != "" {
							var br int64
							fmt.Sscanf(stream.BitRate, "%d", &br)
							meta.AudioBitrate = fmt.Sprintf("%d Kbps", br/1000)
						}
					}
				}
				if meta.Duration == 0 && data.Format.Duration != "" {
					var dur float64
					fmt.Sscanf(data.Format.Duration, "%f", &dur)
					meta.Duration = dur
				}
				if data.Format.BitRate != "" && meta.VideoBitrate == "" {
					var br int64
					fmt.Sscanf(data.Format.BitRate, "%d", &br)
					meta.VideoBitrate = fmt.Sprintf("%d Kbps", br/1000)
				}
			}
		}

		if meta.Width > 0 && meta.Height > 0 {
			meta.AspectRatio = getAspectRatio(meta.Width, meta.Height)
		}

		// B. ffmpeg 第一帧等比压缩截图 (适配横屏/竖屏缩放到最大 320x320，-q:v 5 压缩质量保证极小体积)
		thumbPath := filepath.Join(cacheDir, sha1Val+"_thumb.jpg")
		cmdThumb := exec.Command("ffmpeg", "-y", "-ss", "00:00:00", "-i", filePath, "-vf", "scale='if(gt(iw,ih),320,-1)':'if(gt(iw,ih),-1,320)'", "-vframes", "1", "-q:v", "5", thumbPath)
		if err := cmdThumb.Run(); err == nil {
			meta.ThumbPath = thumbPath
		}

	} else if isImageFile {
		meta.FileType = "image"
		// A. 解析基本图像信息
		imgFile, err := os.Open(filePath)
		if err == nil {
			config, formatName, err := image.DecodeConfig(imgFile)
			_ = imgFile.Close()
			if err == nil {
				meta.Width = config.Width
				meta.Height = config.Height
				meta.ImageCodec = formatName
				meta.Resolution = fmt.Sprintf("%dx%d", config.Width, config.Height)
				if config.Width > 0 && config.Height > 0 {
					meta.AspectRatio = getAspectRatio(config.Width, config.Height)
				}
			}
		}

		// B. EXIF 地理坐标提取
		if geo, err := getGPSInfo(filePath); err == nil {
			meta.GeoInfo = geo
		}

		// C. ffmpeg 导出图片缩略图 (限制 320x320 JPEG)
		thumbPath := filepath.Join(cacheDir, sha1Val+"_thumb.jpg")
		cmdThumb := exec.Command("ffmpeg", "-y", "-i", filePath, "-vf", "scale='if(gt(iw,ih),320,-1)':'if(gt(iw,ih),-1,320)'", "-q:v", "5", thumbPath)
		if err := cmdThumb.Run(); err == nil {
			meta.ThumbPath = thumbPath
		}

		// D. 原图超限检测与转码压缩 (_tran.jpg)
		w, h := float64(meta.Width), float64(meta.Height)
		needTran := false
		if meta.FileSize > 10*1024*1024 { // 10MB 限制
			needTran = true
		}
		if w+h > 10000 { // 宽高之和不能超过 10,000
			needTran = true
		}
		ratio := w / h
		if ratio > 20.0 || ratio < 0.05 { // 长宽比限制
			needTran = true
		}

		if needTran {
			tranPath := filepath.Join(cacheDir, sha1Val+"_tran.jpg")
			var filter []string

			// 比例裁剪 (20:1 限制)
			if ratio > 20.0 {
				filter = append(filter, fmt.Sprintf("crop=%.0f:ih", h*20.0))
				w = h * 20.0
			} else if ratio < 0.05 {
				filter = append(filter, fmt.Sprintf("crop=iw:%.0f", w*20.0))
				h = w * 20.0
			}

			// 像素等比缩放
			if w+h > 9900.0 {
				factor := 9900.0 / (w + h)
				filter = append(filter, fmt.Sprintf("scale=%.0f:%.0f", w*factor, h*factor))
			}

			args := []string{"-y", "-i", filePath}
			if len(filter) > 0 {
				args = append(args, "-vf", strings.Join(filter, ","))
			}
			args = append(args, "-q:v", "5", tranPath)

			cmdTran := exec.Command("ffmpeg", args...)
			if err := cmdTran.Run(); err == nil {
				meta.TranPath = tranPath
			}
		}
	}

	// 3. 将结果写成缓存 json
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err == nil {
		_ = os.WriteFile(jsonPath, metaJSON, 0644)
	}

	return meta, nil
}

func formatSize(bytes int64) string {
	if bytes >= 1024*1024*1024 {
		return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
	}
	return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
}

func naturalLess(s1, s2 string) bool {
	len1, len2 := len(s1), len(s2)
	i, j := 0, 0
	for i < len1 && j < len2 {
		c1, c2 := s1[i], s2[j]
		if isDigit(c1) && isDigit(c2) {
			var numStr1, numStr2 []byte
			for i < len1 && isDigit(s1[i]) {
				numStr1 = append(numStr1, s1[i])
				i++
			}
			for j < len2 && isDigit(s2[j]) {
				numStr2 = append(numStr2, s2[j])
				j++
			}
			n1 := strings.TrimLeft(string(numStr1), "0")
			n2 := strings.TrimLeft(string(numStr2), "0")
			if len(n1) != len(n2) {
				return len(n1) < len(n2)
			}
			if n1 != n2 {
				return n1 < n2
			}
			if len(numStr1) != len(numStr2) {
				return len(numStr1) < len(numStr2)
			}
		} else {
			if c1 != c2 {
				return c1 < c2
			}
			i++
			j++
		}
	}
	return len1 < len2
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
