# Telegram Bot API 媒体与文档上传限制及频率控制指南

本文档汇总了 Telegram Bot API 在媒体（图片、视频、缩略图等）与文档传输、接口调用频控、以及自建 Local Server 模式下的所有官方限制与约束条件，并深入剖析了视频在线即时播放标准及视频旋转拉伸问题的成因与解决方案。此文档旨在为开发人员及其他 AI 智能体/模型提供背景知识，以便于优化备份、分发及大文件传输策略。

---

## 1. 文件与媒体总体积限制 (File Size Limits)

Telegram 对于 Bot 上传和下载文件的体积有非常严格的限制。具体的阈值取决于 Bot 是使用**官方 API 服务器**还是**自建的本地 API 服务器 (Local Bot API Server)**。

| 传输维度 | 官方 API 服务器限制 | 自建本地 API 服务器限制 | 备注 |
| :--- | :--- | :--- | :--- |
| **单文件上传 (Upload)** | **50 MB** | **2000 MB (2 GB)** | 官方对 Bot 的单次 HTTP Post 体积上限限制极严。 |
| **单文件下载 (Download)** | **20 MB** | **2000 MB (2 GB)** | 使用 `getFile` 获取链接并下载的上限。 |
| **使用 file_id 转发** | **2000 MB (2 GB)** | **2000 MB (2 GB)** | 若文件已被其他用户或 Bot 上传过，Bot 可直接通过 file_id 转发大文件。 |

> [!NOTE]
> **用户客户端限制**：Telegram 普通用户通过客户端（使用 TDLib/MTProto 协议而非 HTTP Bot API）最大可上传 **2 GB** 的文件。开通了 Telegram Premium 的用户最大可上传 **4 GB** 的文件。但无论如何，**Bot 的最大上传极限被锁死在 2 GB**（即使采用自建 Local API 服务也无法窥越该协议硬性上限）。

---

## 2. 图片媒体传输技术规范与硬性限制 (Photo Media Limits)

当使用 `sendPhoto` 接口或在 `sendMediaGroup` 中将文件作为“图片”发送时，必须满足以下条件，否则接口会报错拒绝接收：

*   **体积上限**：官方 API 限制单张图片最大为 **10 MB**。
*   **分辨率限制**：单张图片的宽与高像素值相加之和**不能超过 10,000 像素**（即 `width + height <= 10000`）。
*   **长宽比限制 (Aspect Ratio)**：图片的长宽比必须在 **1:20 到 20:1 之间**。如果宽高比例差值大于 20 倍（例如一条极细极长的全景图，宽高比为 `21.0` 或极扁的图为 `0.04`），Telegram 将无法作为 Photo 发送，必须降级为 `Document` 附件形式上传。
*   **支持格式**：主要为 **JPEG**, **PNG**, **静态 WEBP**。其他不常见图像格式（如 TIFF、HEIC、BMP、GIF 动画）上传时应作为 `Document` 传输，或在本地转码为 JPEG 格式。

---

## 3. 视频媒体传输规范与在线即时播放要求 (Video & Inline Streaming Limits)

当使用 `sendVideo` 接口或在 `sendMediaGroup` 中将文件作为“视频”发送时，如果期望用户在客户端中能**免下载直接点击在线即时播放 (Inline Streaming / 边下边播)**，视频必须严格符合以下三项黄金硬性指标：

### A. 编码及封装规范 (Codecs & Container)
*   **封装格式**：必须为 **MP4** 或 **MOV**。
*   **视频编码 (Video Codec)**：必须为 **H.264 / AVC** (建议使用 High Profile 或 Main Profile，色彩空间采用标准 **8-bit YUV420p**)。不支持 H.265/HEVC、AV1、VP9 或 10-bit 色彩（`yuv420p10le`），这些编码在 Telegram 移动端播放时常会出现“黑屏有声”或只能作为普通文件下载播放。
*   **音频编码 (Audio Codec)**：必须为 **AAC**（低复杂度 AAC-LC 最佳）或者无音频轨。不支持 AC3、DTS、TrueHD 等多声道或高码率音频编码。

### B. 播放索引 (moov atom) 的 Faststart 优化
*   在默认情况下，许多剪辑软件或压制工具在生成 MP4 时，会把媒体索引数据（`moov` atom）写入到文件的末尾。
*   **在线播放阻碍**：当客户端从文件的第 0 个字节开始拉取数据流时，如果头部没有发现 `moov` 索引，播放器就无法解析视频的帧结构，必须被迫把整个视频文件完全下载完毕、读到文件末尾的 `moov` 后才能起播。
*   **Faststart 优化**：必须使用工具将 `moov` 索引移至文件最头部（即 Faststart 模式），实现流式加载和“即点即播”。

### C. 接口参数传导 (API Parameters)
*   调用 API 时，必须正确计算并传递视频的物理宽高 `width`、`height` 以及时长 `duration`。如果不传这些元数据，有些客户端（如 iOS 版）将无法唤起内置的视频播放器进行预览。

---

## 4. 视频/文档缩略图（Thumbnail）技术要求

为视频或大文档提供生成预览的缩略图（`thumbnail`）时，API 对缩略图文件有严格的尺寸和格式校验：

*   **格式限制**：必须是 **JPEG** 格式。不支持 PNG、WEBP 等其他格式的图片作为缩略图。
*   **分辨率限制**：缩略图的最大分辨率（长边）**不得超过 320 像素**（例如 `320x180` 或 `180x320`）。
*   **体积限制**：缩略图文件大小**不得超过 200 KB**。
*   **长宽比匹配**：缩略图的长宽比应与视频/文档的**实际物理展示长宽比**保持绝对一致，否则客户端可能会在预览占位时发生拉伸或画面裁剪错乱。

---

## 5. MP4 视频旋转（Rotation）与横屏拉伸问题深度解析

### A. 问题的起因（Rotation 元数据与拉伸）
很多手机录制的视频（尤其是竖屏录制的视频）或剪辑工具导出的媒体，其实物理像素的排列依然是横屏的（例如 `1920x1080`），但是视频轨道的元数据中包含了一个**旋转标志 (Rotation Matrix / Rotate Tag)**，例如 `rotate=90`。
部分 Telegram 客户端的内置播放器在渲染此类视频时，存在比例解析漏洞：
1. 如果在发送 API 参数中，程序传递的 `width` 和 `height` 是未应用旋转的原始尺寸（如 `1920` 和 `1080`），而视频流在渲染时应用了旋转（变成竖屏展示），Telegram 客户端会把旋转后的竖屏画面（`1080x1920`）硬性塞入到 `1920x1080` 的横屏比例容器中，从而导致**视频播放时被严重压扁或拉伸变形**。
2. 同理，如果生成的缩略图（如 `320x180`）与旋转后的物理尺寸比例不一致，也会引起封面预览阶段的拉伸错乱。

### B. 软件工程纠偏方案（gotg 工具实现）
为了彻底解决此问题，本工具在扫描媒体元数据时：
1. **智能捕获旋转**：通过 `ffprobe` 解析视频流的 `Side Data` 中的 `Display Matrix` 旋转角度或 `tags` 中的 `rotate` 属性。
2. **物理宽高对调**：如果旋转角度绝对值为 **90度** 或 **270度**，程序在向 Telegram 传递 API 字段 `width` 和 `height` 时，**自动对调原始的宽高**。
   *   *例如：原始流为 `1920x1080`，旋转角度为 `90`。程序传给 Telegram 的参数会被纠偏为 `width = 1080, height = 1920`*。这能完美指示 Telegram 客户端按竖屏比例渲染，避免拉伸。
3. **匹配缩略图尺寸**：生成的 JPEG 缩略图的分辨率计算也基于翻转后的宽高（例如 `180x320`），确保缩略图与视频物理外观完美贴合。

### C. 终极物理转码纠偏方案（物理重定像素旋转）
如果由于部分特定播放器（如网页端或老旧客户端）彻底忽略旋转元数据，导致画面即使对调宽高也依然横躺，或者比例错乱，唯一的解决办法是**通过 FFmpeg 滤镜直接物理重组像素排列，并彻底清除旋转元数据标签**。

以顺时针旋转90度并消除元数据为例，转换命令如下：
```bash
ffmpeg -i input.mp4 -vf "transpose=1" -c:v libx264 -pix_fmt yuv420p -c:a aac -metadata:s:v rotate="" -movflags +faststart output.mp4
```
*   `-vf "transpose=1"`：顺时针物理旋转 90 度并重排像素，物理分辨率从 `1920x1080` 真正变为 `1080x1920`。
*   `-metadata:s:v rotate=""`：清空视频轨的旋转元数据标签，防止客户端再次二次旋转。
*   `-movflags +faststart`：同时将 `moov` 写入头部，实现在线即时起播。

---

## 6. 不支持播放视频的推荐转换 FFmpeg 命令

若本地视频编码为 H.265/HEVC 等不符合在线播放要求的格式，推荐使用以下命令转码为 Telegram 最佳兼容格式：

### A. 无损 Faststart 重排（仅需移动 moov，视频已是 H.264/AAC）
```bash
ffmpeg -i input.mp4 -c copy -movflags +faststart output.mp4
```
*无损流拷贝，耗时仅需几秒钟。*

### B. 万能兼容转码命令（CPU 软件编码，质量极佳，兼容性极强）
```bash
ffmpeg -i input.mp4 -c:v libx264 -pix_fmt yuv420p -c:a aac -movflags +faststart output.mp4
```

### C. 硬件加速转码命令（极大节省转码时间）
*   **Mac 平台 (Apple Silicon 芯片硬件加速)**：
    ```bash
    ffmpeg -i input.mp4 -c:v h264_videotoolbox -pix_fmt yuv420p -c:a aac -movflags +faststart output.mp4
    ```
*   **NVIDIA GPU (CUDA 显卡加速)**：
    ```bash
    ffmpeg -i input.mp4 -c:v h264_nvenc -pix_fmt yuv420p -c:a aac -movflags +faststart output.mp4
    ```

---

## 7. 媒体组打包限制 (Media Group / Album Limits)

使用 `sendMediaGroup` 接口可以将多个媒体打包成一个相册（Album）进行群发：

*   **成员数量上限**：单个媒体组（Album）最多只能容纳 **10 个媒体文件**（照片或视频）。
*   **混合类型规范**：媒体组内仅支持照片（`InputMediaPhoto`）与视频（`InputMediaVideo`）类型的媒体，无法直接通过该接口打包发送普通的非媒体文档（如 `.zip`、`.pdf`、`.apk` 等文件）。
*   **标题与附言 (Caption)**：媒体组内的每个元素都可以带有独立的标题（caption），但通常为了美观及兼容主流客户端呈现，备份脚本通常只把整组的大标题写在**组内的第一个媒体成员**上。

---

## 8. 接口调用频控与洪泛限制 (Rate Limits & Flooding Control)

Telegram 实施了严格的洪泛控制（Flooding Control），一旦 Bot 请求过于密集，接口将返回 `HTTP 429 Too Many Requests` 或者 `HTTP 400 Bad Request: Too Many Requests: retry after X`。

*   **单群组/频道发信频率**：对单个群组、频道或私聊会话，Bot 每分钟最多只能发送 **20 条消息**。
*   **单 Bot 全局发信频率**：单个 Bot 对所有不同用户/群组的全局发信频次，限制在每秒最多 **30 条消息**。
*   **媒体组 (sendMediaGroup) 的高敏感性**：由于单个媒体组内包含多个大体积附件，调用 `sendMediaGroup` 对 Telegram 服务器造成的 IO 压力极大，频繁调用该接口会在远未达到全局 30条/秒 限制前就触发频控限制。
*   **惩罚等待时长 (Retry-After)**：触发频控后，Telegram 返回的 JSON 报错中会指出需要等待的秒数（从几秒到几十秒不等，常见如 `retry after 8`、`retry after 35`）。在此期间，Bot 发送的任何写请求都会被无条件拒绝。

---

## 9. `gotg` 工具针对上述限制的工程优化策略

本项目（`gotg` 运维及媒体备份工具）通过一系列精细的退避与分包算法，完美解决了上述 Telegram API 限制：

### A. 自动超限风控拦截
在网络上传发送前，程序先读取本地文件元数据的大小。如果检测到未配置自定义 API（限额 50MB）或配置了自定义 API（限额 2GB），且单文件体积超标，则会**直接在本地拦截并输出警告**，绝对不发起无效的 HTTP 请求，避免网络拥堵与资源空转。

### B. 智能频控退避重试 (Too Many Requests Retry)
如果 TG 官方接口返回包含 `Too Many Requests: retry after X` 的频控错误，上传调度器会**自动在本地挂起并精准休眠 35 秒（或 X 秒）后，自动进行二次重试**。若二次重试依然失败，则跳过该组，保证任务整体不中断挂死。

### C. 媒体组“分包分裂”降级上传 (Group Partition Fallback)
由于单个大媒体组（如 10 个视频文件）中可能包含某个未知特殊损坏文件导致 `sendMediaGroup` 整体被 API 拒绝，`gotg` 实现了**分裂退避机制**：
*   当大组首轮重试失败后，程序会在本地将其**等分为 3 个子媒体组**（例如 10 个媒体拆分为 3 + 3 + 4 格式）进行独立下载并分批发送。
*   这保障了即便其中某一组因为某个损坏文件失败，另外 2 个子组的健康文件仍能成功备份，将损失降到最低。

### D. 多 Bot 负载轮询均衡 (Token Rotation `-r`)
如果用户在 `.env` 中部署了多个 Bot Token 并在启动时加入了 `-r`（递归模式）参数，程序在每次发送完一个媒体组并休眠后，会**自动轮换至下一个 Bot Token** 进行下一次发送，从而将针对单一 Bot 的频控流量稀释到原来的 $1/N$，成倍提升大批量备份时的稳定性与总吞吐量。
