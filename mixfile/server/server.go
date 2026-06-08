package server

import (
	"fmt"
	"mixfile-go/mixfile"
	"mixfile-go/mixfile/shareinfo"
	"mixfile-go/mixfile/utils"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

type MixFileServer struct {
	HttpClient        *http.Client
	DownloadTaskCount int
}

func (s *MixFileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 简单模拟路由
	if r.URL.Path == "/api/download" {
		s.handleDownload(w, r)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (s *MixFileServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	shareInfoData := query.Get("s")
	if shareInfoData == "" {
		http.Error(w, "分享信息为空", http.StatusInternalServerError)
		return
	}

	// 1. 解析 ShareInfo (使用之前写的 FromString)
	shareInfo, err := shareinfo.FromString(shareInfoData)
	if err != nil {
		http.Error(w, "解析文件失败", http.StatusInternalServerError)
		return
	}

	// 2. 获取 MixFile 索引
	referer := query.Get("referer")
	if referer == "" {
		referer = shareInfo.Referer
	}

	mixFileBytes, err := shareInfo.DoFetchFile(s.HttpClient, shareInfo.URL, referer)
	if err != nil {
		http.Error(w, "解析文件索引失败", http.StatusInternalServerError)
		return
	}
	mixFile, err := mixfile.FromBytes(mixFileBytes)
	if err != nil {
		http.Error(w, "解析文件索引失败", http.StatusInternalServerError)
		return
	}

	// 3. 处理 Header 和 Range
	name := query.Get("name")
	if name == "" {
		name = shareInfo.FileName
	}

	totalFileSize := mixFile.FileSize
	// 1. 先计算好所有的值，但不要急着设置
	var statusCode = http.StatusOK
	contentLength := totalFileSize
	startRange := int64(0)

	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && strings.HasPrefix(rangeHeader, "bytes=") {
		rangeValue := strings.TrimPrefix(rangeHeader, "bytes=")
		// 即使是 "441843712-"，Split 也会返回 ["441843712", ""]
		parts := strings.Split(rangeValue, "-")

		if len(parts) > 0 && parts[0] != "" {
			start, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
			if err == nil && start >= 0 && start < totalFileSize {
				startRange = start
				statusCode = http.StatusPartialContent

				// 计算实际要发送的长度
				contentLength = totalFileSize - startRange

				// 设置 Content-Range (bytes start-end/total)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d",
					startRange, totalFileSize-1, totalFileSize))
			}
		}
	}

	// 2. 统一设置 Header (在 WriteHeader 之前)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", url.PathEscape(name)))
	w.Header().Set("x-mixfile-code", shareInfo.CachedCode)

	// 3. 最后发送状态码
	w.WriteHeader(statusCode)

	// 4. 开始流式并发下载
	s.writeMixFile(w, shareInfo, mixFile, startRange, referer)
}

func (s *MixFileServer) writeMixFile(w http.ResponseWriter, shareInfo *shareinfo.MixShareInfo, mixFile *mixfile.MixFile, startRange int64, referer string) {
	fileList := mixFile.GetFileListByStartRange(startRange)

	// 计算并发数
	chunkSizeMB := mixFile.ChunkSize / (1024 * 1024)
	if chunkSizeMB < 1 {
		chunkSizeMB = 1
	}
	taskLimit := s.DownloadTaskCount / chunkSizeMB
	if taskLimit < 1 {
		taskLimit = 1
	}

	st := utils.NewSortedTask(taskLimit)
	var wg sync.WaitGroup

	for i, fileRange := range fileList {
		order := i
		targetURL := fileRange.URL
		offset := fileRange.Offset

		if !st.Acquire() {
			// 序列已中止（某个分片失败），停止提交新任务
			break
		}
		wg.Add(1)

		go func(o int, u string, off int) {
			defer wg.Done()
			// 槽位所有权归本协程：无论下载失败、写出失败还是序列中止，
			// 都在退出时归还，保证每次 Acquire 恰好对应一次 Release。
			defer st.Release()

			// 并发下载数据
			data, err := shareInfo.DoFetchFile(s.HttpClient, u, referer)
			if err != nil {
				fmt.Println("分片下载失败: ", err)
				// 流式顺序写出：分片失败必须整体中止，否则后续协程会永久等待序号。
				st.Abort()
				return
			}

			// 按照顺序写入 Response
			err = st.AddAndExecute(o, func() error {
				finalData, sErr := sliceByOffset(data, off)
				if sErr != nil {
					return sErr
				}
				_, wErr := w.Write(finalData)
				return wErr
			})
			//if err != nil {
			//	fmt.Println("分片写出失败: ", err)
			//}
		}(order, targetURL, offset)
	}

	wg.Wait()
}

// sliceByOffset 按偏移量裁剪分片数据，对标 Kotlin 中对 ByteBuffer 的处理：
//
//	off > 0  -> buffer.position(off)        跳过头部（仅第一个分片需要）
//	off < 0  -> buffer.limit(size + off)    截掉尾部（一般不出现）
//	off == 0 -> 原样写出
//
// off == len(data) 时返回空切片（对标 position 到末尾、remaining == 0）。
// 越界则报错中止，避免裸切片在协程内 panic 导致整个服务崩溃。
func sliceByOffset(data []byte, off int) ([]byte, error) {
	switch {
	case off > 0:
		if off > len(data) {
			return nil, fmt.Errorf("分片偏移越界: offset=%d size=%d", off, len(data))
		}
		return data[off:], nil
	case off < 0:
		end := len(data) + off
		if end < 0 {
			return nil, fmt.Errorf("分片尾部裁剪越界: offset=%d size=%d", off, len(data))
		}
		return data[:end], nil
	default:
		return data, nil
	}
}
