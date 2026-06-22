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
	"strconv"
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
	MediaGroupID string    `json:"media_group_id,omitempty"`
	FileID       string    `json:"file_id,omitempty"`

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

type rawFile struct {
	path        string
	name        string
	size        int64
	modTime     time.Time
	createdTime time.Time
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
			if currentTitle == "" {
				subTitle = ""
			} else if currentTitle == "__use_file_name__" {
				subTitle = "__use_file_name__"
			}
			if err := scan(subPath, subTitle, level+1); err != nil {
				return err
			}
		}

		return nil
	}

	err := scan(targetPath, baseTitle, 1)
	return tasks, err
}

func processSingleDir(dirPath string, cacheDir string, forceUp bool, sortType string) ([]rawFile, error) {
	fileInfo, err := os.Stat(dirPath)
	if err != nil {
		return nil, err
	}

	if !fileInfo.IsDir() {
		// 单文件支持
		if !isMediaFile(fileInfo.Name()) {
			return nil, fmt.Errorf("指定的路径不是支持的媒体格式文件: %s", fileInfo.Name())
		}

		createdTime := getBirthTime(fileInfo)
		sha1Val := calculateSHA1(fileInfo.Name(), createdTime, fileInfo.ModTime(), fileInfo.Size())

		if !forceUp {
			if isSuccessInCache(cacheDir, sha1Val) {
				return nil, nil // 已经上传成功过
			}
		}

		return []rawFile{
			{
				path:        dirPath,
				name:        fileInfo.Name(),
				size:        fileInfo.Size(),
				modTime:     fileInfo.ModTime(),
				createdTime: createdTime,
			},
		}, nil
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	var files []rawFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		if !isMediaFile(entry.Name()) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// 慢速遍历控制：每次扫描完一个文件休眠 100 毫秒，防止 WebDAV 密集 API 限制/风控
		time.Sleep(100 * time.Millisecond)

		filePath := filepath.Join(dirPath, entry.Name())
		createdTime := getBirthTime(info)
		sha1Val := calculateSHA1(entry.Name(), createdTime, info.ModTime(), info.Size())

		if !forceUp {
			if isSuccessInCache(cacheDir, sha1Val) {
				continue
			}
		}

		files = append(files, rawFile{
			path:        filePath,
			name:        entry.Name(),
			size:        info.Size(),
			modTime:     info.ModTime(),
			createdTime: createdTime,
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

func isThumbnailBlack(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return true
	}

	bounds := img.Bounds()
	totalPixels := bounds.Dx() * bounds.Dy()
	if totalPixels == 0 {
		return true
	}

	var totalLuminance float64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			rf := float64(r >> 8)
			gf := float64(g >> 8)
			bf := float64(b >> 8)
			totalLuminance += 0.299*rf + 0.587*gf + 0.114*bf
		}
	}

	avgLuminance := totalLuminance / float64(totalPixels)
	return avgLuminance < 10.0
}

type prefetchResult struct {
	processedFiles []uploadFile
	err            error
}

func prefetchBatch(candidateRawFiles []rawFile, cacheDir string, cacheFresh bool, transcode bool, forceUp bool, thumbMinSizeMB int) <-chan prefetchResult {
	ch := make(chan prefetchResult, 1)
	go func() {
		var processedFiles []uploadFile
		for _, raw := range candidateRawFiles {
			// 慢速处理以避免太密集的 API 限制，每文件间隔 100ms
			time.Sleep(100 * time.Millisecond)

			info, err := os.Stat(raw.path)
			if err != nil {
				if isWebDAVIOError(err) {
					ch <- prefetchResult{err: err}
					close(ch)
					return
				}
				fmt.Printf("  ⚠️  文件不可达 (%s): %v，跳过\n", raw.name, err)
				continue
			}

			meta, err := processMedia(raw.path, info, cacheDir, cacheFresh, transcode, forceUp, thumbMinSizeMB)
			if err != nil {
				if isWebDAVIOError(err) {
					ch <- prefetchResult{err: err}
					close(ch)
					return
				}
				fmt.Printf("  ⚠️  媒体预处理失败 (%s): %v，将跳过此不支持的文件\n", raw.name, err)
				continue
			}

			if meta.FileType == "other" {
				fmt.Printf("  ℹ️  非媒体格式文件且不支持转码，自动跳过: %s\n", raw.name)
				continue
			}

			if !forceUp && meta.UploadStatus == "success" {
				fmt.Printf("  ℹ️  文件已上传成功，自动跳过: %s\n", raw.name)
				continue
			}

			processedFiles = append(processedFiles, uploadFile{
				path:        meta.FilePath,
				name:        raw.name,
				size:        meta.FileSize,
				modTime:     meta.ModTime,
				createdTime: meta.CreatedTime,
				metadata:    meta,
			})
		}
		ch <- prefetchResult{processedFiles: processedFiles}
		close(ch)
	}()
	return ch
}

func askForTitleRule() int {
	fmt.Println("\n❓ 请选择标题命名规则 (5秒内无输入默认选择 0):")
	fmt.Println("  1. 使用二级目录名称作为标题 (child-dir-name)")
	fmt.Println("  2. 使用文件名作为标题 (file-name)")
	fmt.Println("  3. 自定义输入标题 (input-name)")
	fmt.Println("  0. 不设置标题 (默认)")
	fmt.Print("请输入选项数字 [0-3]: ")

	ch := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		ch <- strings.TrimSpace(input)
	}()

	select {
	case res := <-ch:
		switch res {
		case "1":
			return 1
		case "2":
			return 2
		case "3":
			return 3
		default:
			return 0
		}
	case <-time.After(5 * time.Second):
		fmt.Println("\n⏰ 5秒超时无输入，已默认选择 0。")
		return 0
	}
}

func getDisplayTitleBase(titleBase string) string {
	if titleBase == "__use_file_name__" {
		return "[使用文件名]"
	}
	if titleBase == "" {
		return "[不设置标题]"
	}
	return titleBase
}

// UploadDirectoryFiles 读取 targetPath 下的多层文件并上传到指定频道
func UploadDirectoryFiles(tokens []string, useRRotation bool, chatIDStr string, notifyID string, targetPath string, apiURL string, groupSize int, debugMode string, sortType string, cacheDir string, cacheFresh bool, sleepTime int, uploadTitle string, isTitleSpecified bool, uploadTag string, forceUp bool, transcode bool, thumbMinSizeMB int) error {
	cleanPath := filepath.Clean(targetPath)
	fileInfo, err := os.Stat(cleanPath)
	if err != nil {
		if isWebDAVIOError(err) {
			sendAlertNotification(tokens, apiURL, notifyID, err.Error(), targetPath)
		}
		return fmt.Errorf("路径不存在或无法访问: %w", err)
	}

	startTime := time.Now()

	// 统计变量定义
	totalSuccessCount := 0
	totalFailedCount := 0
	totalGroupsCount := 0
	successGroupsCount := 0
	failedGroupsCount := 0

	type failedGroupInfo struct {
		Title string
		Files []string
		Err   string
	}
	var failedGroups []failedGroupInfo

	// 确定标题规则
	titleRule := 0 // 默认不设置标题
	customInputTitle := ""

	if isTitleSpecified {
		titleRule = 3
		customInputTitle = uploadTitle
	} else if debugMode == "" || debugMode == "list" || debugMode == "curl" {
		titleRule = askForTitleRule()
		if titleRule == 3 {
			fmt.Print("📝 请输入自定义标题: ")
			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			customInputTitle = strings.TrimSpace(input)
			if customInputTitle == "" {
				fmt.Println("⚠️  输入为空，自动选择 0 (不设置标题)。")
				titleRule = 0
			}
		}
	}

	var allDirTasks []DirTask

	if !fileInfo.IsDir() {
		// 单文件支持
		baseTitle := ""
		switch titleRule {
		case 1, 2:
			// 单文件时使用文件名
			baseTitle = strings.TrimSuffix(filepath.Base(cleanPath), filepath.Ext(cleanPath))
		case 3:
			baseTitle = customInputTitle
		case 0:
			baseTitle = ""
		}

		allDirTasks = []DirTask{
			{
				Path:      cleanPath,
				TitleBase: baseTitle,
			},
		}
	} else {
		// 1. 扫描 cleanPath 下直属的二级子目录
		entries, err := os.ReadDir(cleanPath)
		if err != nil {
			if isWebDAVIOError(err) {
				sendAlertNotification(tokens, apiURL, notifyID, err.Error(), targetPath)
			}
			return fmt.Errorf("读取目录失败: %w", err)
		}

		var secondLevelDirs []os.DirEntry
		for _, entry := range entries {
			if entry.IsDir() {
				secondLevelDirs = append(secondLevelDirs, entry)
			}
		}

		// 检查 cleanPath 目录下是否直接含有媒体文件
		hasDirectMediaFiles := false
		for _, entry := range entries {
			if !entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && isMediaFile(entry.Name()) {
				hasDirectMediaFiles = true
				break
			}
		}

		if len(secondLevelDirs) == 0 {
			rootTitleBase := ""
			switch titleRule {
			case 1:
				rootTitleBase = filepath.Base(cleanPath)
			case 2:
				rootTitleBase = "__use_file_name__"
			case 3:
				rootTitleBase = customInputTitle
			case 0:
				rootTitleBase = ""
			}
			tasksForThisDir, err := scanAndCollectDirPaths(cleanPath, rootTitleBase)
			if err != nil {
				if isWebDAVIOError(err) {
					sendAlertNotification(tokens, apiURL, notifyID, err.Error(), targetPath)
				}
				return fmt.Errorf("读取目录 '%s' 的结构失败: %w", cleanPath, err)
			}
			allDirTasks = append(allDirTasks, tasksForThisDir...)
		} else {
			if hasDirectMediaFiles {
				rootTitleBase := ""
				switch titleRule {
				case 1:
					rootTitleBase = filepath.Base(cleanPath)
				case 2:
					rootTitleBase = "__use_file_name__"
				case 3:
					rootTitleBase = customInputTitle
				case 0:
					rootTitleBase = ""
				}
				allDirTasks = append(allDirTasks, DirTask{
					Path:      cleanPath,
					TitleBase: rootTitleBase,
				})
			}

			// 为每个二级子目录计算 TitleBase
			var secondLevelBaseTitles []string
			for idx, d := range secondLevelDirs {
				titleForThisDir := ""
				switch titleRule {
				case 1:
					titleForThisDir = d.Name()
				case 2:
					titleForThisDir = "__use_file_name__"
				case 3:
					if len(secondLevelDirs) == 1 {
						titleForThisDir = customInputTitle
					} else {
						if idx == 0 {
							titleForThisDir = customInputTitle
						} else {
							titleForThisDir = fmt.Sprintf("%s_%d", customInputTitle, idx+1)
						}
					}
				case 0:
					titleForThisDir = ""
				}
				secondLevelBaseTitles = append(secondLevelBaseTitles, titleForThisDir)
			}

			// 极速探测各二级目录及子目录下的结构树，并生成任务
			for idx, d := range secondLevelDirs {
				subDirPath := filepath.Join(cleanPath, d.Name())
				baseTitleForThisDir := secondLevelBaseTitles[idx]

				tasksForThisDir, err := scanAndCollectDirPaths(subDirPath, baseTitleForThisDir)
				if err != nil {
					if isWebDAVIOError(err) {
						sendAlertNotification(tokens, apiURL, notifyID, err.Error(), targetPath)
					}
					return fmt.Errorf("读取二级子目录 '%s' 的结构失败: %w", d.Name(), err)
				}
				allDirTasks = append(allDirTasks, tasksForThisDir...)
			}
		}
	}

	if len(allDirTasks) == 0 {
		fmt.Println("没有符合上传条件的目录或文件。")
		return nil
	}

	// 4. 调试模式：list
	if debugMode == "list" {
		fmt.Printf("📂 [DEBUG 模式] 扫描目录: %s (共找到 %d 个包含待上传媒体的目录任务, 分组大小: %d)\n", cleanPath, len(allDirTasks), groupSize)
		for _, task := range allDirTasks {
			fmt.Printf("\n🔍 正在读取目录: %s (对应标题前缀: %s)\n", task.Path, getDisplayTitleBase(task.TitleBase))
			
			rawFiles, err := processSingleDir(task.Path, cacheDir, forceUp, sortType)
			if err != nil {
				fmt.Printf("❌ 读取目录 %s 失败: %v\n", task.Path, err)
				continue
			}

			totalRaw := len(rawFiles)
			if totalRaw == 0 {
				fmt.Println("  ℹ️  该目录下无可上传媒体文件或已全部上传成功。")
				continue
			}

			currentIndex := 0
			groupNum := 1

			for currentIndex < totalRaw {
				endIndex := currentIndex + groupSize
				if endIndex > totalRaw {
					endIndex = totalRaw
				}
				candidateRawFiles := rawFiles[currentIndex:endIndex]
				currentIndex = endIndex

				var processedFiles []uploadFile
				for _, raw := range candidateRawFiles {
					info, err := os.Stat(raw.path)
					if err != nil {
						continue
					}
					meta, err := processMedia(raw.path, info, cacheDir, cacheFresh, transcode, forceUp, thumbMinSizeMB)
					if err != nil {
						continue
					}
					if meta.FileType == "other" {
						continue
					}
					if !forceUp && meta.UploadStatus == "success" {
						continue
					}

					processedFiles = append(processedFiles, uploadFile{
						path:        meta.FilePath,
						name:        raw.name,
						size:        meta.FileSize,
						modTime:     meta.ModTime,
						createdTime: meta.CreatedTime,
						metadata:    meta,
					})
				}

				if len(processedFiles) == 0 {
					continue
				}

				batches := splitIntoBatches(processedFiles, groupSize, apiURL)
				if len(batches) == 0 {
					continue
				}

				totalBatchesForGroup := len(batches)
				for _, batchFiles := range batches {
					batchTitle := task.TitleBase
					if task.TitleBase == "__use_file_name__" && len(batchFiles) > 0 {
						firstFile := batchFiles[0].name
						batchTitle = strings.TrimSuffix(firstFile, filepath.Ext(firstFile))
					}
					isSingleGroup := (totalRaw <= groupSize && totalBatchesForGroup == 1)
					if !isSingleGroup {
						batchTitle += fmt.Sprintf("（%d）", groupNum)
					}
					groupNum++

					if uploadTag != "" {
						batchTitle += "\n" + uploadTag
					}

					fmt.Printf("  📦 [模拟组] 发送标题为:\n\"\"\"\n%s\n\"\"\"\n", batchTitle)
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
		for _, task := range allDirTasks {
			fmt.Printf("\n🔍 正在读取目录: %s (标题前缀: %s)\n", task.Path, getDisplayTitleBase(task.TitleBase))
			
			rawFiles, err := processSingleDir(task.Path, cacheDir, forceUp, sortType)
			if err != nil {
				fmt.Printf("❌ 读取目录 %s 失败: %v\n", task.Path, err)
				continue
			}

			totalRaw := len(rawFiles)
			if totalRaw == 0 {
				fmt.Println("  ℹ️  该目录下无可上传媒体文件或已全部上传成功。")
				continue
			}

			currentIndex := 0
			groupNum := 1

			for currentIndex < totalRaw {
				endIndex := currentIndex + groupSize
				if endIndex > totalRaw {
					endIndex = totalRaw
				}
				candidateRawFiles := rawFiles[currentIndex:endIndex]
				currentIndex = endIndex

				var processedFiles []uploadFile
				for _, raw := range candidateRawFiles {
					info, err := os.Stat(raw.path)
					if err != nil {
						continue
					}
					meta, err := processMedia(raw.path, info, cacheDir, cacheFresh, transcode, forceUp, thumbMinSizeMB)
					if err != nil {
						continue
					}
					if meta.FileType == "other" {
						continue
					}
					if !forceUp && meta.UploadStatus == "success" {
						continue
					}

					processedFiles = append(processedFiles, uploadFile{
						path:        meta.FilePath,
						name:        raw.name,
						size:        meta.FileSize,
						modTime:     meta.ModTime,
						createdTime: meta.CreatedTime,
						metadata:    meta,
					})
				}

				if len(processedFiles) == 0 {
					continue
				}

				batches := splitIntoBatches(processedFiles, groupSize, apiURL)
				if len(batches) == 0 {
					continue
				}

				totalBatchesForGroup := len(batches)
				for subBatchIdx, batchFiles := range batches {
					// 计算 Token
					currToken := tokens[tokenIdx]
					apiEndpoint := fmt.Sprintf("%s/bot%s/sendMediaGroup", baseAPI, currToken)

					// 拼装标题
					batchTitle := task.TitleBase
					if task.TitleBase == "__use_file_name__" && len(batchFiles) > 0 {
						firstFile := batchFiles[0].name
						batchTitle = strings.TrimSuffix(firstFile, filepath.Ext(firstFile))
					}
					isSingleGroup := (totalRaw <= groupSize && totalBatchesForGroup == 1)
					if !isSingleGroup {
						batchTitle += fmt.Sprintf("（%d）", groupNum)
					}
					groupNum++

					if uploadTag != "" {
						batchTitle += "\n" + uploadTag
					}

					fmt.Printf("\n📦 [组模拟] (文件数: %d) Token: %s curl 命令模拟:\n", len(batchFiles), maskToken(currToken))

					var mediaItems []string
					var filesFields []string
					for mediaIdx, f := range batchFiles {
						attachName := fmt.Sprintf("file%d", mediaIdx)
						uploadPath := f.path
						if f.metadata.TranPath != "" {
							uploadPath = f.metadata.TranPath
						}

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

							if f.metadata.ThumbPath != "" {
								thumbAttachName := fmt.Sprintf("thumb_photo%d", mediaIdx)
								filesFields = append(filesFields, fmt.Sprintf(`     -F "%s=@%s"`, thumbAttachName, f.metadata.ThumbPath))
							}
							filesFields = append(filesFields, fmt.Sprintf(`     -F "%s=@%s"`, attachName, uploadPath))
						} else {
							filesFields = append(filesFields, fmt.Sprintf(`     -F "%s=@%s"`, attachName, uploadPath))
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

					isLastBatchOverall := (currentIndex >= totalRaw && subBatchIdx == totalBatchesForGroup-1)
					if !isLastBatchOverall {
						fmt.Printf("💤 [CURL 模式] 每组输出后模拟休眠 %d 秒...\n", sleepTime)
					}
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

	chatID := parseChatID(chatIDStr)
	tokenIdx := 0
	count := 0

	for idx, task := range allDirTasks {
		fmt.Printf("\n🔍 [%d/%d] 正在读取目录: %s (标题前缀: %s)\n", idx+1, len(allDirTasks), task.Path, getDisplayTitleBase(task.TitleBase))
		
		rawFiles, err := processSingleDir(task.Path, cacheDir, forceUp, sortType)
		if err != nil {
			if isWebDAVIOError(err) {
				sendAlertNotification(tokens, apiURL, notifyID, err.Error(), targetPath)
				return fmt.Errorf("读取目录失败 (WebDAV/IO 错误): %w", err)
			}
			fmt.Printf("❌ 读取目录 %s 失败: %v\n", task.Path, err)
			continue
		}

		totalRaw := len(rawFiles)
		if totalRaw == 0 {
			fmt.Println("  ℹ️  该目录下无可上传媒体文件或已全部上传成功。")
			continue
		}

		fmt.Printf("📂 该目录下共找到 %d 个待上传文件，正在按分包大小 (最多 %d) 逐步预处理并上传...\n", totalRaw, groupSize)

		currentIndex := 0
		groupNum := 1

		var nextBatchChan <-chan prefetchResult
		if currentIndex < totalRaw {
			endIndex := currentIndex + groupSize
			if endIndex > totalRaw {
				endIndex = totalRaw
			}
			candidateRawFiles := rawFiles[currentIndex:endIndex]
			currentIndex = endIndex
			nextBatchChan = prefetchBatch(candidateRawFiles, cacheDir, cacheFresh, transcode, forceUp, thumbMinSizeMB)
		}

		for nextBatchChan != nil {
			res := <-nextBatchChan
			if res.err != nil && isWebDAVIOError(res.err) {
				sendAlertNotification(tokens, apiURL, notifyID, res.err.Error(), targetPath)
				return fmt.Errorf("预处理文件失败 (WebDAV/IO 错误): %w", res.err)
			}
			processedFiles := res.processedFiles

			var currentNextBatchChan <-chan prefetchResult
			if currentIndex < totalRaw {
				endIndex := currentIndex + groupSize
				if endIndex > totalRaw {
					endIndex = totalRaw
				}
				candidateRawFiles := rawFiles[currentIndex:endIndex]
				currentIndex = endIndex
				currentNextBatchChan = prefetchBatch(candidateRawFiles, cacheDir, cacheFresh, transcode, forceUp, thumbMinSizeMB)
			}
			nextBatchChan = currentNextBatchChan

			if len(processedFiles) == 0 {
				continue
			}

			batches := splitIntoBatches(processedFiles, groupSize, apiURL)
			if len(batches) == 0 {
				continue
			}

			totalBatchesForGroup := len(batches)
			for subBatchIdx, batch := range batches {
				// 拼装这组的标题
				batchTitle := task.TitleBase
				if task.TitleBase == "__use_file_name__" && len(batch) > 0 {
					firstFile := batch[0].name
					batchTitle = strings.TrimSuffix(firstFile, filepath.Ext(firstFile))
				}
				isSingleGroup := (totalRaw <= groupSize && totalBatchesForGroup == 1)
				if !isSingleGroup {
					batchTitle += fmt.Sprintf("（%d）", groupNum)
				}
				groupNum++

				if uploadTag != "" {
					batchTitle += "\n" + uploadTag
				}

				// 切换 Bot
				bot := bots[tokenIdx]
				token := tokens[tokenIdx]

				var totalBytes int64
				for _, f := range batch {
					totalBytes += f.size
				}

				fmt.Printf("🚀 正在以媒体组上传 %d 个文件 (使用 Bot Token: %s)...\n", len(batch), maskToken(token))

				var uploadedBytes int64
				var currentFile string
				var mu sync.Mutex

				var openFiles []*os.File
				var mediaList []telego.InputMedia
				var openErr error

				for mediaIdx, f := range batch {
					uploadPath := f.path
					if f.metadata.TranPath != "" {
						uploadPath = f.metadata.TranPath
						fmt.Printf("  ⚠️  原图规格超限，已自动切换为转码图: %s\n", filepath.Base(uploadPath))
					}

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
					if isWebDAVIOError(openErr) {
						sendAlertNotification(tokens, apiURL, notifyID, openErr.Error(), targetPath)
						return fmt.Errorf("读取文件失败 (WebDAV/IO 错误): %w", openErr)
					}
					fmt.Println("❌ 上传组失败: 无法打开部分文件")
					totalGroupsCount++
					failedGroupsCount++
					var fileNames []string
					for _, f := range batch {
						fileNames = append(fileNames, f.name)
						totalFailedCount++
					}
					failedGroups = append(failedGroups, failedGroupInfo{
						Title: batchTitle,
						Files: fileNames,
						Err:   "打开文件失败: " + openErr.Error(),
					})
					continue
				}

				totalGroupsCount++
				var messages []telego.Message
				messages, err = bot.SendMediaGroup(context.Background(), &telego.SendMediaGroupParams{
					ChatID: chatID,
					Media:  mediaList,
				})

				// 无论是成功还是发生普通错误，先关闭本轮文件句柄
				for _, fh := range openFiles {
					_ = fh.Close()
				}

				if debugMode == "" {
					fmt.Println()
				}

				if err != nil {
					// 1. 如果是 WebDAV / IO 超时或系统错误，立即报警并退出任务
					if isWebDAVIOError(err) {
						sendAlertNotification(tokens, apiURL, notifyID, err.Error(), targetPath)
						return fmt.Errorf("上传媒体组失败 (WebDAV/IO 错误): %w", err)
					}

					// 2. 如果是正常的 TG API 报错，等待 35 秒后发起重试
					fmt.Printf("⚠️ 媒体组上传失败: %v。将在 35 秒后重新尝试上传...\n", err)
					time.Sleep(35 * time.Second)

					uploadedBytes = 0 // 重置进度，以支持重试时绘制进度条

					var retryOpenFiles []*os.File
					var retryMediaList []telego.InputMedia
					var retryOpenErr error

					for mediaIdx, f := range batch {
						uploadPath := f.path
						if f.metadata.TranPath != "" {
							uploadPath = f.metadata.TranPath
						}

						caption := ""
						if mediaIdx == 0 {
							caption = batchTitle
						}

						fileHandle, err := os.Open(uploadPath)
						if err != nil {
							retryOpenErr = err
							break
						}
						retryOpenFiles = append(retryOpenFiles, fileHandle)

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
								if err == nil {
									retryOpenFiles = append(retryOpenFiles, thumbHandle)
									doc.Thumbnail = &telego.InputFile{File: thumbHandle}
								}
							}
							retryMediaList = append(retryMediaList, doc)
						} else if f.metadata.FileType == "image" {
							doc := &telego.InputMediaPhoto{
								Type:    "photo",
								Media:   telego.InputFile{File: reader},
								Caption: caption,
							}
							retryMediaList = append(retryMediaList, doc)
						} else {
							doc := &telego.InputMediaDocument{
								Type:    "document",
								Media:   telego.InputFile{File: reader},
								Caption: caption,
							}
							if f.metadata.ThumbPath != "" {
								thumbHandle, err := os.Open(f.metadata.ThumbPath)
								if err == nil {
									retryOpenFiles = append(retryOpenFiles, thumbHandle)
									doc.Thumbnail = &telego.InputFile{File: thumbHandle}
								}
							}
							retryMediaList = append(retryMediaList, doc)
						}
					}

					if retryOpenErr != nil {
						for _, fh := range retryOpenFiles {
							_ = fh.Close()
						}
						if isWebDAVIOError(retryOpenErr) {
							sendAlertNotification(tokens, apiURL, notifyID, retryOpenErr.Error(), targetPath)
							return fmt.Errorf("读取文件失败 (WebDAV/IO 错误): %w", retryOpenErr)
						}
						err = fmt.Errorf("重试时无法打开部分文件: %w", retryOpenErr)
					} else {
						fmt.Println("🔄 正在重新发起上传...")
						var retryErr error
						var messagesResult []telego.Message
						messagesResult, retryErr = bot.SendMediaGroup(context.Background(), &telego.SendMediaGroupParams{
							ChatID: chatID,
							Media:  retryMediaList,
						})

						for _, fh := range retryOpenFiles {
							_ = fh.Close()
						}

						if retryErr != nil && isWebDAVIOError(retryErr) {
							sendAlertNotification(tokens, apiURL, notifyID, retryErr.Error(), targetPath)
							return fmt.Errorf("上传媒体组失败 (WebDAV/IO 错误): %w", retryErr)
						}
						err = retryErr
						if err == nil {
							messages = messagesResult
						}
					}
				}

				if err != nil {
					fmt.Printf("❌ 媒体组重试上传依然失败: %v\n", err)
					for _, f := range batch {
						updateCacheStatus(cacheDir, f.metadata.SHA1, "failed")
					}
					failedGroupsCount++
					var fileNames []string
					for _, f := range batch {
						fileNames = append(fileNames, f.name)
						totalFailedCount++
					}
					failedGroups = append(failedGroups, failedGroupInfo{
						Title: batchTitle,
						Files: fileNames,
						Err:   err.Error(),
					})
				} else {
					fmt.Printf("✅ 媒体组上传成功 (%d 个文件)\n", len(batch))
					count += len(batch)
					for i, f := range batch {
						mediaGroupID := ""
						fileID := ""
						if i < len(messages) {
							mediaGroupID = messages[i].MediaGroupID
							fileID = getFileIDFromMessage(messages[i])
						}
						updateCacheSuccess(cacheDir, f.metadata.SHA1, mediaGroupID, fileID)
						cleanTranscodeFiles(cacheDir, f.metadata)
					}
					successGroupsCount++
					totalSuccessCount += len(batch)
				}

				if useRRotation && len(bots) > 1 {
					tokenIdx = (tokenIdx + 1) % len(bots)
					fmt.Printf("🔄 轮询模式：切换到下一个 Bot (Token: %s)\n", maskToken(tokens[tokenIdx]))
				}

				isLastBatchOverall := (currentIndex >= totalRaw && subBatchIdx == totalBatchesForGroup-1)
				if !isLastBatchOverall || (useRRotation && len(bots) > 1) {
					fmt.Printf("💤 已完成该媒体组处理，根据设置休眠 %d 秒以防止 API 频控/风控...\n", sleepTime)
					time.Sleep(time.Duration(sleepTime) * time.Second)
				}
			}
		}
	}

	fmt.Printf("\n🎉 所有目录的上传任务结束，成功上传了 %d 个文件。\n", count)

	// 发送任务完成报告
	if notifyID != "" && debugMode == "" && len(bots) > 0 {
		duration := time.Since(startTime)
		durStr := formatDuration(duration)

		hostname, hostnameErr := os.Hostname()
		if hostnameErr != nil {
			hostname = "unknown-host"
		}

		loc := time.FixedZone("CST", 8*3600) // 东八区
		endTimeStr := time.Now().In(loc).Format("2006-01-02 15:04:05")

		var sb strings.Builder
		sb.WriteString("🔔 <b>gotg 媒体上传任务已完成</b>\n\n")
		sb.WriteString(fmt.Sprintf("💻 <b>执行服务器</b>：<code>%s</code>\n", escapeHTML(hostname)))
		sb.WriteString(fmt.Sprintf("📂 <b>目标路径</b>：<code>%s</code>\n", escapeHTML(targetPath)))
		sb.WriteString(fmt.Sprintf("📅 <b>完成时间</b>：<code>%s (UTC+8)</code>\n", endTimeStr))
		sb.WriteString(fmt.Sprintf("⏱ <b>任务耗时</b>：<code>%s</code>\n", durStr))
		sb.WriteString(fmt.Sprintf("📊 <b>成功上传</b>：<code>%d</code> 个文件\n", totalSuccessCount))
		if totalFailedCount > 0 {
			sb.WriteString(fmt.Sprintf("❌ <b>失败文件</b>：<code>%d</code> 个\n", totalFailedCount))
		}
		sb.WriteString(fmt.Sprintf("📦 <b>媒体组统计</b>：成功 <code>%d</code> 组 / 失败 <code>%d</code> 组 / 总共 <code>%d</code> 组\n", successGroupsCount, failedGroupsCount, totalGroupsCount))

		if len(failedGroups) > 0 {
			sb.WriteString("\n⚠️ <b>失败资源明细</b>：\n")
			for i, fg := range failedGroups {
				titleText := fg.Title
				if titleText == "" {
					titleText = "(无标题)"
				}
				sb.WriteString(fmt.Sprintf("\n<b>%d. 组标题：%s</b>\n", i+1, escapeHTML(titleText)))
				sb.WriteString(fmt.Sprintf("   原因：<code>%s</code>\n", escapeHTML(fg.Err)))
				sb.WriteString("   文件列表：\n")
				for _, f := range fg.Files {
					sb.WriteString(fmt.Sprintf("   ├─ <code>%s</code>\n", escapeHTML(f)))
				}
			}
		}

		targetChatID := parseChatID(notifyID)
		fmt.Printf("\n🔄 正在向通知接收人 (%s) 发送任务完成报告...\n", notifyID)
		_, sendErr := bots[0].SendMessage(context.Background(), &telego.SendMessageParams{
			ChatID:    targetChatID,
			Text:      sb.String(),
			ParseMode: telego.ModeHTML,
		})
		if sendErr != nil {
			fmt.Printf("⚠️ 报告发送失败: %v\n", sendErr)
		} else {
			fmt.Println("✅ 完成报告已成功发送至您的 Telegram！")
		}
	}

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

func processMedia(filePath string, info os.FileInfo, cacheDir string, cacheFresh bool, transcode bool, forceUp bool, thumbMinSizeMB int) (MediaMetadata, error) {
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
		cmd := exec.Command("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", meta.FilePath)
		output, err := cmd.Output()
		if err == nil {
			var data FFProbeResult
			if err := json.Unmarshal(output, &data); err == nil {
				for _, stream := range data.Streams {
					if stream.CodecType == "video" {
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

						absRot := rotation
						if absRot < 0 {
							absRot = -absRot
						}
						// 如果旋转了 90 或 270 度，则物理展示的宽高在提交给 TG 时需要翻转
						if absRot == 90 || absRot == 270 {
							meta.Width = stream.Height
							meta.Height = stream.Width
						} else {
							meta.Width = stream.Width
							meta.Height = stream.Height
						}

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

					cmdProbe := exec.Command("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", h264Path)
					outputProbe, errProbe := cmdProbe.Output()
					if errProbe == nil {
						var data FFProbeResult
						if err := json.Unmarshal(outputProbe, &data); err == nil {
							for _, stream := range data.Streams {
								if stream.CodecType == "video" {
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

									absRot := rotation
									if absRot < 0 {
										absRot = -absRot
									}
									// 如果旋转了 90 或 270 度，则物理展示的宽高在提交给 TG 时需要翻转
									if absRot == 90 || absRot == 270 {
										meta.Width = stream.Height
										meta.Height = stream.Width
									} else {
										meta.Width = stream.Width
										meta.Height = stream.Height
									}

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
			fmt.Printf("🎬 正在为视频生成缩略图: %s...\n", filepath.Base(meta.FilePath))
			thumbPath := filepath.Join(cacheDir, sha1Val+"_thumb.jpg")
			
			// A. 默认在首帧（00:00:00）截图
			cmdThumb := exec.Command("ffmpeg", "-y", "-ss", "00:00:00", "-i", meta.FilePath, "-vf", "scale='if(gt(iw,ih),320,-1)':'if(gt(iw,ih),-1,320)'", "-vframes", "1", "-q:v", "5", thumbPath)
			if err := cmdThumb.Run(); err == nil {
				// B. 检测截图是否为纯黑屏。如果是，且时长足够，则退回到第 1 秒截图
				if isThumbnailBlack(thumbPath) && meta.Duration > 1.0 {
					fmt.Printf("ℹ️  视频首帧截图为纯黑，正在尝试在第 1 秒处重新生成缩略图: %s...\n", filepath.Base(meta.FilePath))
					cmdThumb1s := exec.Command("ffmpeg", "-y", "-ss", "00:00:01", "-i", meta.FilePath, "-vf", "scale='if(gt(iw,ih),320,-1)':'if(gt(iw,ih),-1,320)'", "-vframes", "1", "-q:v", "5", thumbPath)
					if err1s := cmdThumb1s.Run(); err1s == nil {
						meta.ThumbPath = thumbPath
					} else {
						fmt.Printf("⚠️  视频第 1 秒缩略图重新生成失败: %v\n", err1s)
						meta.ThumbPath = thumbPath // 依然保留首帧图作为底限
					}
				} else {
					meta.ThumbPath = thumbPath
				}
			} else {
				fmt.Printf("⚠️  视频首帧缩略图生成失败: %v\n", err)
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

		// C. ffmpeg 导出图片缩略图 (限制 320x320 JPEG，且仅在图片体积超过指定 MB 时生成)
		thumbSizeLimit := int64(thumbMinSizeMB) * 1024 * 1024
		if meta.FileSize > thumbSizeLimit {
			fmt.Printf("🎨 正在为图片生成缩略图: %s (%s)...\n", filepath.Base(filePath), formatSize(meta.FileSize))
			thumbPath := filepath.Join(cacheDir, sha1Val+"_thumb.jpg")
			cmdThumb := exec.Command("ffmpeg", "-y", "-i", filePath, "-vf", "scale='if(gt(iw,ih),320,-1)':'if(gt(iw,ih),-1,320)'", "-q:v", "5", thumbPath)
			if err := cmdThumb.Run(); err == nil {
				meta.ThumbPath = thumbPath
			} else {
				fmt.Printf("⚠️  图片缩略图生成失败: %v\n", err)
			}
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
	// 3. 如果音频编码存在但不是 aac，且不是苹果常见的免转码音频格式（针对 mov 容器豁免），必转
	aCodec := strings.ToLower(meta.AudioCodec)
	if aCodec != "" && aCodec != "aac" {
		if ext == ".mov" {
			switch aCodec {
			case "pcm_s16le", "pcm_s24le", "alac", "lpcm":
				// 苹果设备原生常见的高质量音轨编码，免转码直传
				return false
			}
		}
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

// splitIntoBatches 根据文件大小上限 (1.9GB) 和最大数量限制，智能地对文件列表进行多组切分
func splitIntoBatches(files []uploadFile, groupSize int, apiURL string) [][]uploadFile {
	var eligibleFiles []uploadFile
	limit := int64(50 * 1024 * 1024)
	limitStr := "50MB"
	if apiURL != "" {
		limit = 2000 * 1024 * 1024
		limitStr = "2GB"
	}

	for _, f := range files {
		realUploadSize := f.size
		if f.metadata.TranPath != "" {
			if tranInfo, err := os.Stat(f.metadata.TranPath); err == nil {
				realUploadSize = tranInfo.Size()
			}
		}

		if realUploadSize > limit {
			fmt.Printf("❌ 跳过 %s (上传体积 %s 超过 %s 限制)\n", f.name, formatSize(realUploadSize), limitStr)
			continue
		}

		f.size = realUploadSize
		eligibleFiles = append(eligibleFiles, f)
	}

	var batches [][]uploadFile
	maxGroupBytes := int64(1900 * 1024 * 1024) // 1.9GB，安全阀值，预留富余应对 HTTP 协议头

	var currentBatch []uploadFile
	var currentBatchBytes int64

	for _, f := range eligibleFiles {
		// 若当前组大小已满，或者加入该文件会使这组总体积超过 1.9GB，则存入当前组并开启新组
		if len(currentBatch) >= groupSize || (len(currentBatch) > 0 && currentBatchBytes+f.size > maxGroupBytes) {
			batches = append(batches, currentBatch)
			currentBatch = []uploadFile{f}
			currentBatchBytes = f.size
		} else {
			currentBatch = append(currentBatch, f)
			currentBatchBytes += f.size
		}
	}
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}
	return batches
}

func countFiles(batches [][]uploadFile) int {
	total := 0
	for _, b := range batches {
		total += len(b)
	}
	return total
}

func isSuccessInCache(cacheDir string, sha1Val string) bool {
	jsonPath := filepath.Join(cacheDir, sha1Val+".json")
	if cachedData, err := os.ReadFile(jsonPath); err == nil {
		var meta MediaMetadata
		if err := json.Unmarshal(cachedData, &meta); err == nil {
			if meta.UploadStatus == "success" {
				return true
			}
		}
	}
	return false
}

func isMediaFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	isPotentialVideoFile := (ext == ".mp4" || ext == ".mov" || ext == ".avi" || ext == ".mpeg" || ext == ".mpg" || ext == ".flv" || ext == ".m4v" || ext == ".ts" || ext == ".mkv" || ext == ".wmv" || ext == ".rmvb" || ext == ".3gp")
	isImageFile := (ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp")
	return isPotentialVideoFile || isImageFile
}

func parseChatID(str string) telego.ChatID {
	if id, err := strconv.ParseInt(str, 10, 64); err == nil {
		return telego.ChatID{ID: id}
	}
	return telego.ChatID{Username: str}
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func isWebDAVIOError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	// 如果是 Telegram API 明确返回的错误，排除在 WebDAV IO 错误之外
	if strings.Contains(errStr, "api:") || strings.Contains(errStr, "bad request") || strings.Contains(errStr, "too many requests") {
		return false
	}
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "timed out") ||
		strings.Contains(errStr, "operation not supported") ||
		strings.Contains(errStr, "input/output error") ||
		strings.Contains(errStr, "transport endpoint is not connected") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "i/o error") ||
		strings.Contains(errStr, "state not recoverable")
}

func sendAlertNotification(tokens []string, apiURL string, notifyID string, errMsg string, targetPath string) {
	if len(tokens) == 0 || notifyID == "" {
		return
	}
	var opts []telego.BotOption
	opts = append(opts, telego.WithDefaultLogger(false, false))
	if apiURL != "" {
		opts = append(opts, telego.WithAPIServer(apiURL))
	}
	bot, err := telego.NewBot(tokens[0], opts...)
	if err != nil {
		fmt.Printf("⚠️ 报警 Bot 初始化失败: %v\n", err)
		return
	}

	hostname, hostnameErr := os.Hostname()
	if hostnameErr != nil {
		hostname = "unknown-host"
	}

	loc := time.FixedZone("CST", 8*3600) // 东八区
	timeStr := time.Now().In(loc).Format("2006-01-02 15:04:05")

	var sb strings.Builder
	sb.WriteString("🚨 <b>gotg 任务因 WebDAV / IO 异常异常中止</b>\n\n")
	sb.WriteString(fmt.Sprintf("💻 <b>执行服务器</b>：<code>%s</code>\n", escapeHTML(hostname)))
	sb.WriteString(fmt.Sprintf("📂 <b>目标路径</b>：<code>%s</code>\n", escapeHTML(targetPath)))
	sb.WriteString(fmt.Sprintf("📅 <b>发生时间</b>：<code>%s (UTC+8)</code>\n", timeStr))
	sb.WriteString(fmt.Sprintf("❌ <b>错误信息</b>：<code>%s</code>\n", escapeHTML(errMsg)))

	targetChatID := parseChatID(notifyID)
	fmt.Printf("\n🔄 正在向通知接收人 (%s) 发送 WebDAV/IO 异常报警...\n", notifyID)
	_, sendErr := bot.SendMessage(context.Background(), &telego.SendMessageParams{
		ChatID:    targetChatID,
		Text:      sb.String(),
		ParseMode: telego.ModeHTML,
	})
	if sendErr != nil {
		fmt.Printf("⚠️ 报警发送失败: %v\n", sendErr)
	} else {
		fmt.Println("✅ 异常中止报警已成功发送至您的 Telegram！")
	}
}

func getFileIDFromMessage(msg telego.Message) string {
	if msg.Video != nil {
		return msg.Video.FileID
	}
	if len(msg.Photo) > 0 {
		return msg.Photo[len(msg.Photo)-1].FileID
	}
	if msg.Document != nil {
		return msg.Document.FileID
	}
	if msg.Audio != nil {
		return msg.Audio.FileID
	}
	return ""
}

func updateCacheSuccess(cacheDir string, sha1Val string, mediaGroupID string, fileID string) {
	jsonPath := filepath.Join(cacheDir, sha1Val+".json")
	cachedData, err := os.ReadFile(jsonPath)
	if err != nil {
		return
	}
	var meta MediaMetadata
	if err := json.Unmarshal(cachedData, &meta); err != nil {
		return
	}
	meta.UploadStatus = "success"
	meta.UploadTime = time.Now()
	meta.MediaGroupID = mediaGroupID
	meta.FileID = fileID

	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err == nil {
		_ = os.WriteFile(jsonPath, metaJSON, 0644)
	}
}
