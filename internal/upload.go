package internal

import (
	"bufio"
	"context" // 💡 引入 context 包
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"  // 注册 GIF 解码器
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
	"github.com/rwcarlsen/goexif/exif"
)

type MediaMetadata struct {
	SHA1         string    `json:"sha1"`
	FileName     string    `json:"file_name"`
	FilePath     string    `json:"file_path"`
	FileSize     int64     `json:"file_size"`
	ModTime      time.Time `json:"mod_time"`
	CreatedTime  time.Time `json:"created_time"`
	FileType     string    `json:"file_type"` // "video", "image", "other"
	UploadStatus string    `json:"upload_status,omitempty"`
	UploadTime   time.Time `json:"upload_time,omitempty"`

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

type DirTask struct {
	Path      string
	TitleBase string
}

func scanAndCollectDirPaths(targetPath string, baseTitle string) ([]DirTask, error) {
	var tasks []DirTask

	var scan func(currentPath string, currentTitle string, level int) error
	scan = func(currentPath string, currentTitle string, level int) error {
		if level > 3 {
			return nil
		}

		entries, err := os.ReadDir(currentPath)
		if err != nil {
			return err
		}

		var hasFiles bool
		var subDirs []os.DirEntry

		for _, entry := range entries {
			if entry.IsDir() {
				if !strings.HasPrefix(entry.Name(), ".") {
					subDirs = append(subDirs, entry)
				}
			} else {
				nameLower := strings.ToLower(entry.Name())
				if !strings.HasPrefix(nameLower, ".") && (strings.HasSuffix(nameLower, ".png") ||
					strings.HasSuffix(nameLower, ".jpg") ||
					strings.HasSuffix(nameLower, ".jpeg") ||
					strings.HasSuffix(nameLower, ".gif") ||
					strings.HasSuffix(nameLower, ".webp") ||
					strings.HasSuffix(nameLower, ".mp4") ||
					strings.HasSuffix(nameLower, ".mov") ||
					strings.HasSuffix(nameLower, ".avi") ||
					strings.HasSuffix(nameLower, ".mpeg") ||
					strings.HasSuffix(nameLower, ".mpg") ||
					strings.HasSuffix(nameLower, ".flv") ||
					strings.HasSuffix(nameLower, ".m4v") ||
					strings.HasSuffix(nameLower, ".ts") ||
					strings.HasSuffix(nameLower, ".mkv") ||
					strings.HasSuffix(nameLower, ".wmv") ||
					strings.HasSuffix(nameLower, ".rmvb") ||
					strings.HasSuffix(nameLower, ".3gp")) {
					hasFiles = true
				}
			}
		}

		if hasFiles {
			tasks = append(tasks, DirTask{
				Path:      currentPath,
				TitleBase: currentTitle,
			})
		}

		for _, subDir := range subDirs {
			subPath := filepath.Join(currentPath, subDir.Name())
			subTitle := currentTitle + "/" + subDir.Name()
			if err := scan(subPath, subTitle, level+1); err != nil {
				return err
			}
		}

		return nil
	}

	err := scan(targetPath, baseTitle, 1)
	return tasks, err
}

func processSingleDir(dirPath string, cacheDir string, cacheFresh bool, transcode bool, forceUp bool, sortType string) ([]uploadFile, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	var files []uploadFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// 慢速遍历控制：每次扫描完一个文件休眠 100 毫秒，防止 WebDAV 密集 API 限制/风控
		time.Sleep(100 * time.Millisecond)

		filePath := filepath.Join(dirPath, entry.Name())
		meta, err := processMedia(filePath, info, cacheDir, cacheFresh, transcode, forceUp)
		if err != nil {
			fmt.Printf("⚠️  媒体预处理失败 (%s): %v，将跳过此不支持的文件\n", entry.Name(), err)
			continue
		}

		if meta.FileType == "other" {
			fmt.Printf("ℹ️  非媒体格式文件且不支持转码，自动跳过: %s\n", entry.Name())
			continue
		}

		if !forceUp && meta.UploadStatus == "success" {
			fmt.Printf("ℹ️  文件已上传成功，自动跳过: %s\n", entry.Name())
			continue
		}

		files = append(files, uploadFile{
			path:        meta.FilePath,
			name:        entry.Name(),
			size:        meta.FileSize,
			modTime:     meta.ModTime,
			createdTime: meta.CreatedTime,
			metadata:    meta,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		f1, f2 := files[i], files[j]
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

	return files, nil
}

// UploadDirectoryFiles 读取 targetPath 下的多层文件并上传到指定频道
func UploadDirectoryFiles(tokens []string, useRRotation bool, chatIDStr string, targetPath string, apiURL string, groupSize int, debugMode string, sortType string, cacheDir string, cacheFresh bool, sleepTime int, uploadTitle string, uploadTag string, forceUp bool, transcode bool) error {
	cleanPath := filepath.Clean(targetPath)
	fileInfo, err := os.Stat(cleanPath)
	if err != nil {
		return fmt.Errorf("路径不存在或无法访问: %w", err)
	}

	if !fileInfo.IsDir() {
		return fmt.Errorf("指定的路径 '%s' 不是一个目录", targetPath)
	}

	// 1. 扫描 cleanPath 下直属的二级子目录
	entries, err := os.ReadDir(cleanPath)
	if err != nil {
		return fmt.Errorf("读取目录失败: %w", err)
	}

	var secondLevelDirs []os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() {
			secondLevelDirs = append(secondLevelDirs, entry)
		}
	}

	if len(secondLevelDirs) == 0 {
		fmt.Printf("ℹ️  目标目录 '%s' 下没有二级子目录，无需执行上传。\n", cleanPath)
		return nil
	}

	// 2. 询问用户标题逻辑
	var secondLevelBaseTitles []string
	if debugMode == "" || debugMode == "list" || debugMode == "curl" {
		if uploadTitle != "" {
			// 如果命令行显式传入了 --title
			if len(secondLevelDirs) == 1 {
				secondLevelBaseTitles = append(secondLevelBaseTitles, uploadTitle)
			} else {
				for idx := range secondLevelDirs {
					if idx == 0 {
						secondLevelBaseTitles = append(secondLevelBaseTitles, uploadTitle)
					} else {
						secondLevelBaseTitles = append(secondLevelBaseTitles, fmt.Sprintf("%s_%d", uploadTitle, idx+1))
					}
				}
			}
		} else {
			fmt.Print("❓ 是否使用二级目录名称作为标题？[Y/n] (输入 n 可自定义输入标题): ")
			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(strings.ToLower(input))

			if input == "n" || input == "no" {
				fmt.Print("📝 请输入自定义标题: ")
				customTitle, _ := reader.ReadString('\n')
				customTitle = strings.TrimSpace(customTitle)
				if customTitle == "" {
					fmt.Println("⚠️  输入为空，自动回退使用各二级目录名称。")
					for _, d := range secondLevelDirs {
						secondLevelBaseTitles = append(secondLevelBaseTitles, d.Name())
					}
				} else {
					if len(secondLevelDirs) == 1 {
						secondLevelBaseTitles = append(secondLevelBaseTitles, customTitle)
					} else {
						for idx := range secondLevelDirs {
							if idx == 0 {
								secondLevelBaseTitles = append(secondLevelBaseTitles, customTitle)
							} else {
								secondLevelBaseTitles = append(secondLevelBaseTitles, fmt.Sprintf("%s_%d", customTitle, idx+1))
							}
						}
					}
				}
			} else {
				for _, d := range secondLevelDirs {
					secondLevelBaseTitles = append(secondLevelBaseTitles, d.Name())
				}
			}
		}
	} else {
		if uploadTitle != "" {
			if len(secondLevelDirs) == 1 {
				secondLevelBaseTitles = append(secondLevelBaseTitles, uploadTitle)
			} else {
				for idx := range secondLevelDirs {
					if idx == 0 {
						secondLevelBaseTitles = append(secondLevelBaseTitles, uploadTitle)
					} else {
						secondLevelBaseTitles = append(secondLevelBaseTitles, fmt.Sprintf("%s_%d", uploadTitle, idx+1))
					}
				}
			}
		} else {
			for _, d := range secondLevelDirs {
				secondLevelBaseTitles = append(secondLevelBaseTitles, d.Name())
			}
		}
	}

	// 3. 极速探测各二级目录及子目录下的结构树，仅扫描路径不读取文件大属性
	var allDirTasks []DirTask
	for idx, d := range secondLevelDirs {
		subDirPath := filepath.Join(cleanPath, d.Name())
		baseTitleForThisDir := secondLevelBaseTitles[idx]

		tasksForThisDir, err := scanAndCollectDirPaths(subDirPath, baseTitleForThisDir)
		if err != nil {
			return fmt.Errorf("读取二级子目录 '%s' 的结构失败: %w", d.Name(), err)
		}
		allDirTasks = append(allDirTasks, tasksForThisDir...)
	}

	if len(allDirTasks) == 0 {
		fmt.Println("没有符合上传条件的目录或文件。")
		return nil
	}

	// 4. 调试模式：list
	if debugMode == "list" {
		fmt.Printf("📂 [DEBUG 模式] 扫描目录: %s (共找到 %d 个包含待上传媒体的目录任务, 分组大小: %d)\n", cleanPath, len(allDirTasks), groupSize)
		for idx, task := range allDirTasks {
			fmt.Printf("\n🔍 正在读取并预处理第 %d/%d 个目录: %s (对应标题前缀: %s)\n", idx+1, len(allDirTasks), task.Path, task.TitleBase)
			
			filesToUpload, err := processSingleDir(task.Path, cacheDir, cacheFresh, transcode, forceUp, sortType)
			if err != nil {
				fmt.Printf("❌ 预处理目录 %s 失败: %v\n", task.Path, err)
				continue
			}

			if len(filesToUpload) == 0 {
				fmt.Println("  ℹ️  该目录下无可上传媒体文件或已全部上传成功。")
				continue
			}

			totalBatches := (len(filesToUpload) + groupSize - 1) / groupSize
			for i := 0; i < len(filesToUpload); i += groupSize {
				end := i + groupSize
				if end > len(filesToUpload) {
					end = len(filesToUpload)
				}
				batchNum := i/groupSize + 1
				batchFiles := filesToUpload[i:end]
				
				// 拼装这组的标题
				batchTitle := task.TitleBase
				if totalBatches > 1 {
					batchTitle += fmt.Sprintf("（%d）", batchNum)
				}
				if uploadTag != "" {
					batchTitle += "\n" + uploadTag
				}

				fmt.Printf("  📦 [组 %d/%d] 发送标题为:\n\"\"\"\n%s\n\"\"\"\n", batchNum, totalBatches, batchTitle)
				for fileIdx, f := range batchFiles {
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
					fmt.Printf("    ├─ %d. %s (%s)%s\n", fileIdx+1, f.name, formatSize(f.size), details)
				}
			}
		}
		return nil
	}

	// 5. 调试模式：curl
	if debugMode == "curl" {
		fmt.Printf("📂 [DEBUG CURL 模式] 扫描目录: %s (共找到 %d 个包含待上传媒体的目录任务)\n", cleanPath, len(allDirTasks))
		
		baseAPI := "https://api.telegram.org"
		if apiURL != "" {
			baseAPI = strings.TrimSuffix(apiURL, "/")
		}

		tokenIdx := 0
		for idx, task := range allDirTasks {
			fmt.Printf("\n🔍 正在读取并预处理第 %d/%d 个目录: %s (标题前缀: %s)\n", idx+1, len(allDirTasks), task.Path, task.TitleBase)
			
			filesToUpload, err := processSingleDir(task.Path, cacheDir, cacheFresh, transcode, forceUp, sortType)
			if err != nil {
				fmt.Printf("❌ 预处理目录 %s 失败: %v\n", task.Path, err)
				continue
			}

			if len(filesToUpload) == 0 {
				fmt.Println("  ℹ️  该目录下无可上传媒体文件或已全部上传成功。")
				continue
			}

			totalBatches := (len(filesToUpload) + groupSize - 1) / groupSize
			for i := 0; i < len(filesToUpload); i += groupSize {
				end := i + groupSize
				if end > len(filesToUpload) {
					end = len(filesToUpload)
				}
				batchNum := i/groupSize + 1
				batchFiles := filesToUpload[i:end]

				// 计算 Token
				currToken := tokens[tokenIdx]
				apiEndpoint := fmt.Sprintf("%s/bot%s/sendMediaGroup", baseAPI, currToken)

				// 拼装标题
				batchTitle := task.TitleBase
				if totalBatches > 1 {
					batchTitle += fmt.Sprintf("（%d）", batchNum)
				}
				if uploadTag != "" {
					batchTitle += "\n" + uploadTag
				}

				fmt.Printf("\n📦 [组 %d/%d] (文件数: %d) Token: %s curl 命令模拟:\n", batchNum, totalBatches, len(batchFiles), maskToken(currToken))

				var mediaItems []string
				var filesFields []string
				for mediaIdx, f := range batchFiles {
					attachName := fmt.Sprintf("file%d", mediaIdx)
					uploadPath := f.path
					if f.metadata.TranPath != "" {
						uploadPath = f.metadata.TranPath
					}

					// 仅给媒体组的第一个文件设置 caption
					escapedCaption := ""
					if mediaIdx == 0 {
						escapedCaption = strings.ReplaceAll(batchTitle, "\n", "\\n")
						escapedCaption = strings.ReplaceAll(escapedCaption, `"`, `\"`)
					}

					captionPart := ""
					if escapedCaption != "" {
						captionPart = fmt.Sprintf(`,
         \"caption\": \"%s\"`, escapedCaption)
					}

					if f.metadata.FileType == "video" {
						thumbAttachName := fmt.Sprintf("thumb_video%d", mediaIdx)
						mediaItems = append(mediaItems, fmt.Sprintf(`       {
         \"type\": \"video\",
         \"media\": \"attach://%s\",
         \"width\": %d,
         \"height\": %d,
         \"duration\": %.0f,
         \"supports_streaming\": true,
         \"thumb\": \"attach://%s\"%s
       }`, attachName, f.metadata.Width, f.metadata.Height, f.metadata.Duration, thumbAttachName, captionPart))

						filesFields = append(filesFields, fmt.Sprintf(`     -F "%s=@%s"`, thumbAttachName, f.metadata.ThumbPath))
						filesFields = append(filesFields, fmt.Sprintf(`     -F "%s=@%s"`, attachName, uploadPath))
					} else if f.metadata.FileType == "image" {
						mediaItems = append(mediaItems, fmt.Sprintf(`       {
         \"type\": \"photo\",
         \"media\": \"attach://%s\"%s
       }`, attachName, captionPart))

						filesFields = append(filesFields, fmt.Sprintf(`     -F "%s=@%s"`, attachName, uploadPath))
					} else {
						// 其他文件降级使用 document 类型
						if f.metadata.ThumbPath != "" {
							thumbAttachName := fmt.Sprintf("thumb_doc%d", mediaIdx)
							mediaItems = append(mediaItems, fmt.Sprintf(`       {
         \"type\": \"document\",
         \"media\": \"attach://%s\",
         \"thumb\": \"attach://%s\"%s
       }`, attachName, thumbAttachName, captionPart))
							filesFields = append(filesFields, fmt.Sprintf(`     -F "%s=@%s"`, thumbAttachName, f.metadata.ThumbPath))
							filesFields = append(filesFields, fmt.Sprintf(`     -F "%s=@%s"`, attachName, uploadPath))
						} else {
							mediaItems = append(mediaItems, fmt.Sprintf(`       {
         \"type\": \"document\",
         \"media\": \"attach://%s\"%s
       }`, attachName, captionPart))
							filesFields = append(filesFields, fmt.Sprintf(`     -F "%s=@%s"`, attachName, uploadPath))
						}
					}
				}

				mediaJSON := fmt.Sprintf("[\n%s\n     ]", strings.Join(mediaItems, ",\n"))

				fmt.Println("```bash")
				fmt.Printf("curl -X POST %q \\\n", apiEndpoint)
				fmt.Printf("     -F \"chat_id=%s\" \\\n", chatIDStr)
				fmt.Printf("     -F \"media=%s\" \\\n", mediaJSON)
				fmt.Println(strings.Join(filesFields, " \\\n"))
				fmt.Println("```")

				if useRRotation && len(tokens) > 1 {
					tokenIdx = (tokenIdx + 1) % len(tokens)
				}

				if i+groupSize < len(filesToUpload) {
					fmt.Printf("💤 [CURL 模式] 每组输出后模拟休眠 %d 秒...\n", sleepTime)
				}
			}
		}
		return nil
	}

	// 6. 实际上传模式
	var bots []*telego.Bot
	for _, t := range tokens {
		var opts []telego.BotOption
		opts = append(opts, telego.WithDefaultLogger(false, false))
		if apiURL != "" {
			opts = append(opts, telego.WithAPIServer(apiURL))
		}
		bot, err := telego.NewBot(t, opts...)
		if err != nil {
			return fmt.Errorf("初始化 Bot 失败: %w", err)
		}
		bots = append(bots, bot)
	}

	chatID := telego.ChatID{Username: chatIDStr}
	tokenIdx := 0
	count := 0

	for idx, task := range allDirTasks {
		fmt.Printf("\n🔍 [%d/%d] 正在读取并预处理目录: %s (标题前缀: %s)\n", idx+1, len(allDirTasks), task.Path, task.TitleBase)
		
		filesToUpload, err := processSingleDir(task.Path, cacheDir, cacheFresh, transcode, forceUp, sortType)
		if err != nil {
			fmt.Printf("❌ 预处理目录 %s 失败: %v\n", task.Path, err)
			continue
		}

		totalFiles := len(filesToUpload)
		if totalFiles == 0 {
			fmt.Println("  ℹ️  该目录下无可上传媒体文件或已全部上传成功。")
			continue
		}

		totalBatches := (totalFiles + groupSize - 1) / groupSize
		fmt.Printf("📂 开始上传该目录，共 %d 个待上传文件\n", totalFiles)

		for i := 0; i < totalFiles; i += groupSize {
			end := i + groupSize
			if end > totalFiles {
				end = totalFiles
			}
			batch := filesToUpload[i:end]
			batchNum := i/groupSize + 1

			// 切换 Bot
			bot := bots[tokenIdx]
			token := tokens[tokenIdx]

			// 验证上传限制
			var validatedBatch []uploadFile
			var totalBytes int64
			for _, f := range batch {
				realUploadSize := f.size
				if f.metadata.TranPath != "" {
					if tranInfo, err := os.Stat(f.metadata.TranPath); err == nil {
						realUploadSize = tranInfo.Size()
					}
				}

				limit := int64(50 * 1024 * 1024)
				limitStr := "50MB"
				if apiURL != "" {
					limit = 2000 * 1024 * 1024
					limitStr = "2GB"
				}

				if realUploadSize > limit {
					fmt.Printf("❌ 跳过 %s (上传体积 %s 超过 %s 限制)\n", f.name, formatSize(realUploadSize), limitStr)
					continue
				}
				
				f.size = realUploadSize // 校正为实际上传大小
				totalBytes += realUploadSize
				validatedBatch = append(validatedBatch, f)
			}

			if len(validatedBatch) == 0 {
				continue
			}

			// 拼装这组的标题
			batchTitle := task.TitleBase
			if totalBatches > 1 {
				batchTitle += fmt.Sprintf("（%d）", batchNum)
			}
			if uploadTag != "" {
				batchTitle += "\n" + uploadTag
			}

			fmt.Printf("🚀 正在以媒体组上传 %d 个文件 (使用 Bot Token: %s)...\n", len(validatedBatch), maskToken(token))

			var uploadedBytes int64
			var currentFile string
			var mu sync.Mutex

			var openFiles []*os.File
			var mediaList []telego.InputMedia
			var openErr error

			for mediaIdx, f := range validatedBatch {
				uploadPath := f.path
				if f.metadata.TranPath != "" {
					uploadPath = f.metadata.TranPath
					fmt.Printf("  ⚠️  原图规格超限，已自动切换为转码图: %s\n", filepath.Base(uploadPath))
				}

				// 仅给媒体组的第一个文件设置 Caption
				caption := ""
				if mediaIdx == 0 {
					caption = batchTitle
				}

				fmt.Printf("  ├─ 准备文件: %s (%s)\n", f.name, formatSize(f.size))
				fileHandle, err := os.Open(uploadPath)
				if err != nil {
					openErr = err
					fmt.Printf("  ❌ 打开文件失败: %v\n", err)
					break
				}
				openFiles = append(openFiles, fileHandle)

				var reader telegoapi.NamedReader = fileHandle
				if debugMode == "" {
					reader = &ProgressNamedReader{
						Reader: fileHandle,
						name:   filepath.Base(uploadPath),
						onRead: func(n int) {
							mu.Lock()
							uploadedBytes += int64(n)
							currentFile = f.name
							drawProgressBar(uploadedBytes, totalBytes, currentFile)
							mu.Unlock()
						},
					}
				}

				if f.metadata.FileType == "video" {
					doc := &telego.InputMediaVideo{
						Type:              "video",
						Media:             telego.InputFile{File: reader},
						Width:             f.metadata.Width,
						Height:            f.metadata.Height,
						Duration:          int(f.metadata.Duration),
						SupportsStreaming: true,
						Caption:           caption,
					}
					if f.metadata.ThumbPath != "" {
						thumbHandle, err := os.Open(f.metadata.ThumbPath)
						if err != nil {
							fmt.Printf("  ⚠️  打开视频缩略图失败: %v\n", err)
						} else {
							openFiles = append(openFiles, thumbHandle)
							doc.Thumbnail = &telego.InputFile{File: thumbHandle}
						}
					}
					mediaList = append(mediaList, doc)

				} else if f.metadata.FileType == "image" {
					doc := &telego.InputMediaPhoto{
						Type:    "photo",
						Media:   telego.InputFile{File: reader},
						Caption: caption,
					}
					mediaList = append(mediaList, doc)

				} else {
					doc := &telego.InputMediaDocument{
						Type:    "document",
						Media:   telego.InputFile{File: reader},
						Caption: caption,
					}
					if f.metadata.ThumbPath != "" {
						thumbHandle, err := os.Open(f.metadata.ThumbPath)
						if err != nil {
							fmt.Printf("  ⚠️  打开文档缩略图失败: %v\n", err)
						} else {
							openFiles = append(openFiles, thumbHandle)
							doc.Thumbnail = &telego.InputFile{File: thumbHandle}
						}
					}
					mediaList = append(mediaList, doc)
				}
			}

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

			for _, fh := range openFiles {
				_ = fh.Close()
			}

			if debugMode == "" {
				fmt.Println()
			}

			if err != nil {
				fmt.Printf("❌ 媒体组上传失败: %v\n", err)
				for _, f := range validatedBatch {
					updateCacheStatus(cacheDir, f.metadata.SHA1, "failed")
				}
			} else {
				fmt.Printf("✅ 媒体组上传成功 (%d 个文件)\n", len(validatedBatch))
				count += len(validatedBatch)
				for _, f := range validatedBatch {
					updateCacheStatus(cacheDir, f.metadata.SHA1, "success")
					cleanTranscodeFiles(cacheDir, f.metadata)
				}
			}

			// 切换 Bot Token
			if useRRotation && len(bots) > 1 {
				tokenIdx = (tokenIdx + 1) % len(bots)
				fmt.Printf("🔄 轮询模式：切换到下一个 Bot (Token: %s)\n", maskToken(tokens[tokenIdx]))
			}

			// 休眠
			if i+groupSize < totalFiles || tokenIdx > 0 {
				fmt.Printf("💤 已完成该媒体组处理，根据设置休眠 %d 秒以防止 API 频控/风控...\n", sleepTime)
				time.Sleep(time.Duration(sleepTime) * time.Second)
			}
		}
	}

	fmt.Printf("\n🎉 所有目录的上传任务结束，成功上传了 %d 个文件。\n", count)
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

func updateCacheStatus(cacheDir string, sha1Val string, status string) {
	jsonPath := filepath.Join(cacheDir, sha1Val+".json")
	cachedData, err := os.ReadFile(jsonPath)
	if err != nil {
		return
	}
	var meta MediaMetadata
	if err := json.Unmarshal(cachedData, &meta); err != nil {
		return
	}
	meta.UploadStatus = status
	if status == "success" {
		meta.UploadTime = time.Now()
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err == nil {
		_ = os.WriteFile(jsonPath, metaJSON, 0644)
	}
}

func processMedia(filePath string, info os.FileInfo, cacheDir string, cacheFresh bool, transcode bool, forceUp bool) (MediaMetadata, error) {
	createdTime := getBirthTime(info)
	sha1Val := calculateSHA1(info.Name(), createdTime, info.ModTime(), info.Size())
	jsonPath := filepath.Join(cacheDir, sha1Val+".json")

	// 1. 如果缓存存在，且不要求刷新，直接读缓存
	if !cacheFresh {
		if cachedData, err := os.ReadFile(jsonPath); err == nil {
			var meta MediaMetadata
			if err := json.Unmarshal(cachedData, &meta); err == nil {
				// 💡 关键优化：如果该文件已经成功上传，且本次没有要求强制重传，
				// 则无需对已删除的临时转码视频物理文件进行 os.Stat 校验，直接以 success 返回以供上层过滤跳过
				if !forceUp && meta.UploadStatus == "success" {
					return meta, nil
				}

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
					localExt := strings.ToLower(filepath.Ext(filePath))
					isVid := (localExt == ".mp4" || localExt == ".mov" || localExt == ".avi" || localExt == ".mpeg" || localExt == ".mpg" || localExt == ".flv" || localExt == ".m4v" || localExt == ".ts" || localExt == ".mkv" || localExt == ".wmv" || localExt == ".rmvb" || localExt == ".3gp")
					if isVid && meta.FileType == "other" {
						// 穿透缓存以重新检测或重新转码
					} else {
						return meta, nil
					}
				}
			}
		}
	}

	// 2. 缓存不存在或需刷新，全新扫描
	ext := strings.ToLower(filepath.Ext(filePath))
	meta := MediaMetadata{
		SHA1:         sha1Val,
		FileName:     info.Name(),
		FilePath:     filePath,
		FileSize:     info.Size(),
		ModTime:      info.ModTime(),
		CreatedTime:  createdTime,
		FileType:     "other",
		UploadStatus: "pending",
	}

	isPotentialVideoFile := (ext == ".mp4" || ext == ".mov" || ext == ".avi" || ext == ".mpeg" || ext == ".mpg" || ext == ".flv" || ext == ".m4v" || ext == ".ts" || ext == ".mkv" || ext == ".wmv" || ext == ".rmvb" || ext == ".3gp")
	isImageFile := (ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp")

	if isPotentialVideoFile {
		meta.FileType = "video"

		h264Path := filepath.Join(cacheDir, sha1Val+"_h264.mp4")
		orgPath := filepath.Join(cacheDir, sha1Val+"_org"+ext)
		transcodingPath := filepath.Join(cacheDir, sha1Val+"_h264_transcoding.mp4")

		useCachedH264 := false
		if _, err := os.Stat(h264Path); err == nil {
			useCachedH264 = true
			meta.FilePath = h264Path
			if h264Info, err := os.Stat(h264Path); err == nil {
				meta.FileSize = h264Info.Size()
			}
		}

		// A. ffprobe 解析视频元数据
		cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height,bit_rate,duration:format=duration,bit_rate", "-of", "json", meta.FilePath)
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

		// B. 如果不是缓存好的 h264 视频，且需要转码，则触发转码
		if !useCachedH264 && needTranscode(meta.FilePath, ext, meta) {
			if _, err := os.Stat(orgPath); err != nil {
				fmt.Printf("📦 正在复制源文件至缓存目录以备转码: %s\n", meta.FileName)
				if err := copyFile(filePath, orgPath); err != nil {
					return meta, fmt.Errorf("复制源视频失败: %w", err)
				}
			}

			doTranscode := transcode
			if !doTranscode {
				doTranscode = askForTranscode(meta.FileName)
			}

			if doTranscode {
				encoder := getBestH264Encoder()
				fmt.Printf("⏳ 正在转码视频为标准 H264 MP4 (编码器: %s): %s ...\n", encoder, meta.FileName)
				args := []string{"-y", "-i", orgPath, "-c:v", encoder, "-pix_fmt", "yuv420p", "-c:a", "aac", transcodingPath}

				fmt.Printf("ℹ️  运行转码命令: ffmpeg %s\n", strings.Join(args, " "))

				cmdTrans := exec.Command("ffmpeg", args...)
				errTrans := cmdTrans.Run()
				if errTrans != nil && encoder != "libx264" {
					fmt.Printf("⚠️  硬件加速编码器 '%s' 转码失败（可能由于缺乏硬件或驱动支持）: %v\n", encoder, errTrans)
					fmt.Println("🔄 正在尝试自动降级到 CPU 软件编码器 (libx264) 重新转码...")
					encoder = "libx264"
					args = []string{"-y", "-i", orgPath, "-c:v", encoder, "-pix_fmt", "yuv420p", "-c:a", "aac", transcodingPath}
					fmt.Printf("ℹ️  运行转码命令: ffmpeg %s\n", strings.Join(args, " "))
					cmdTrans = exec.Command("ffmpeg", args...)
					errTrans = cmdTrans.Run()
				}

				if errTrans == nil {
					_ = os.Rename(transcodingPath, h264Path)
					_ = os.Remove(orgPath)

					fmt.Printf("✅ 视频转码成功: %s\n", meta.FileName)

					meta.FilePath = h264Path
					if h264Info, err := os.Stat(h264Path); err == nil {
						meta.FileSize = h264Info.Size()
					}

					cmdProbe := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height,duration", "-of", "json", h264Path)
					outputProbe, errProbe := cmdProbe.Output()
					if errProbe == nil {
						var data FFProbeResult
						if err := json.Unmarshal(outputProbe, &data); err == nil {
							for _, stream := range data.Streams {
								if stream.CodecType == "video" {
									meta.Width = stream.Width
									meta.Height = stream.Height
									meta.VideoCodec = stream.CodecName
									if stream.Duration != "" {
										var dur float64
										fmt.Sscanf(stream.Duration, "%f", &dur)
										meta.Duration = dur
									}
								} else if stream.CodecType == "audio" {
									meta.AudioCodec = stream.CodecName
								}
							}
						}
					}
					if meta.Width > 0 && meta.Height > 0 {
						meta.AspectRatio = getAspectRatio(meta.Width, meta.Height)
					}
				} else {
					_ = os.Remove(transcodingPath)
					_ = os.Remove(orgPath)
					meta.FileType = "other"
					return meta, fmt.Errorf("视频转码命令执行失败")
				}
			} else {
				_ = os.Remove(orgPath)
				meta.FileType = "other"
				return meta, nil
			}
		}

		if meta.FileType == "video" {
			thumbPath := filepath.Join(cacheDir, sha1Val+"_thumb.jpg")
			cmdThumb := exec.Command("ffmpeg", "-y", "-ss", "00:00:00", "-i", meta.FilePath, "-vf", "scale='if(gt(iw,ih),320,-1)':'if(gt(iw,ih),-1,320)'", "-vframes", "1", "-q:v", "5", thumbPath)
			if err := cmdThumb.Run(); err == nil {
				meta.ThumbPath = thumbPath
			}
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

// ---------------------- 视频转码与辅助函数 ----------------------

func getBestH264Encoder() string {
	cmd := exec.Command("ffmpeg", "-encoders")
	output, err := cmd.Output()
	if err != nil {
		return "libx264"
	}

	encodersStr := string(output)
	if strings.Contains(encodersStr, "h264_videotoolbox") {
		return "h264_videotoolbox"
	}
	if strings.Contains(encodersStr, "h264_nvenc") {
		return "h264_nvenc"
	}
	if strings.Contains(encodersStr, "h264_amf") {
		return "h264_amf"
	}
	if strings.Contains(encodersStr, "h264_qsv") {
		return "h264_qsv"
	}
	return "libx264"
}

func needTranscode(filePath string, ext string, meta MediaMetadata) bool {
	// 1. 如果不是 mp4 且不是 mov，必转
	if ext != ".mp4" && ext != ".mov" {
		return true
	}
	// 2. 如果视频编码不是 h264 且不是 h265，必转
	vCodec := strings.ToLower(meta.VideoCodec)
	if vCodec != "h264" && vCodec != "h265" && vCodec != "hevc" {
		return true
	}
	// 3. 如果音频编码存在但不是 aac，必转
	aCodec := strings.ToLower(meta.AudioCodec)
	if aCodec != "" && aCodec != "aac" {
		return true
	}
	return false
}

func askForTranscode(fileName string) bool {
	fmt.Printf("\n⚠️  视频文件 '%s' 编码格式不符合 Telegram 播放规范，是否需要转码为 H264 MP4 视频？[y/N] (5秒内无输入将自动跳过该文件): ", fileName)

	ch := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		ch <- strings.TrimSpace(strings.ToLower(input))
	}()

	select {
	case res := <-ch:
		return res == "y" || res == "yes"
	case <-time.After(5 * time.Second):
		fmt.Println("\n⏰ 超时未确认，已自动跳过该文件。")
		return false
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func cleanTranscodeFiles(cacheDir string, meta MediaMetadata) {
	// 1. 删除 org 文件
	orgExt := filepath.Ext(meta.FileName)
	orgPath := filepath.Join(cacheDir, meta.SHA1+"_org"+orgExt)
	if _, err := os.Stat(orgPath); err == nil {
		_ = os.Remove(orgPath)
	}

	// 2. 无论FilePath是什么，只要缓存目录下对应的 h264.mp4 存在，就彻底清除它
	h264Path := filepath.Join(cacheDir, meta.SHA1+"_h264.mp4")
	if _, err := os.Stat(h264Path); err == nil {
		_ = os.Remove(h264Path)
	}
}

// ---------------------- 进度条支持 ----------------------

type ProgressNamedReader struct {
	io.Reader
	name   string
	onRead func(n int)
}

func (p *ProgressNamedReader) Name() string {
	return p.name
}

func (p *ProgressNamedReader) Read(buf []byte) (int, error) {
	n, err := p.Reader.Read(buf)
	if n > 0 && p.onRead != nil {
		p.onRead(n)
	}
	return n, err
}

func (p *ProgressNamedReader) Seek(offset int64, whence int) (int64, error) {
	if seeker, ok := p.Reader.(io.Seeker); ok {
		return seeker.Seek(offset, whence)
	}
	return 0, fmt.Errorf("underlying reader does not support seeking")
}

func shrinkFileName(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name
	}
	if maxLen <= 5 {
		return name[:maxLen]
	}
	half := (maxLen - 3) / 2
	tail := maxLen - 3 - half
	return name[:half] + "..." + name[len(name)-tail:]
}

func drawProgressBar(uploaded, total int64, fileName string) {
	if total <= 0 {
		return
	}
	pct := float64(uploaded) * 100 / float64(total)
	if pct > 100 {
		pct = 100
	}

	const barWidth = 20
	completed := int(pct / 100 * barWidth)
	if completed > barWidth {
		completed = barWidth
	}

	bar := make([]byte, barWidth)
	for i := 0; i < barWidth; i++ {
		if i < completed {
			bar[i] = '='
		} else if i == completed && completed < barWidth {
			bar[i] = '>'
		} else {
			bar[i] = '-'
		}
	}

	shrunkName := shrinkFileName(fileName, 25)

	fmt.Printf("\r📤 上传进度: [%s] %3.0f%% (%s / %s) | %-25s",
		string(bar),
		pct,
		formatSize(uploaded),
		formatSize(total),
		shrunkName,
	)
}
