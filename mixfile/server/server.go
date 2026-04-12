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

	shareInfoData = utils.SubstringAfter(shareInfoData, "mf://")

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

	// 假设 fetchFile 逻辑已实现
	mixFileBytes, err := shareInfo.DoFetchFile(s.HttpClient, shareInfo.URL, referer)
	if err != nil {
		http.Error(w, "解析文件索引失败", http.StatusInternalServerError)
		return
	}
	mixFile, _ := mixfile.FromBytes(mixFileBytes)

	// 3. 处理 Header 和 Range
	name := query.Get("name")
	if name == "" {
		name = shareInfo.FileName
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", url.QueryEscape(name)))
	//w.Header().Set("x-mixfile-code", shareInfo.CachedCode)

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
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", url.QueryEscape(name)))
	//w.Header().Set("x-mixfile-code", shareInfo.CachedCode)

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

	// 错误处理通道
	errChan := make(chan error, 1)

	for i, fileRange := range fileList {
		order := i
		targetURL := fileRange.URL
		offset := fileRange.Offset

		st.Acquire()
		wg.Add(1)

		go func(o int, u string, off int) {
			defer wg.Done()

			// 并发下载数据
			data, err := shareInfo.DoFetchFile(s.HttpClient, u, referer)

			if err != nil {
			    fmt.Println("发生错误: ", err)
				select {
				case errChan <- err:
				default:
				}
				return
			}

			// 按照顺序写入 Response
			err = st.AddAndExecute(o, func() error {
				finalData := data
				if off > 0 && off < len(data) {
					finalData = data[off:]
				}
				_, wErr := w.Write(finalData)
				return wErr
			})

			if err != nil {
				select {
				case errChan <- err:
				default:
				}
			}
		}(order, targetURL, offset)

		// 如果发生错误，停止提交新任务
		if len(errChan) > 0 {
			break
		}
	}

	wg.Wait()
}
