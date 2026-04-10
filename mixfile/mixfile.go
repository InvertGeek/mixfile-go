package mixfile

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
)

// MixFile 对应 Kotlin 的 MixFile
type MixFile struct {
	ChunkSize int      `json:"chunk_size"`
	FileSize  int64    `json:"file_size"`
	Version   int64    `json:"version"`
	FileList  []string `json:"file_list"`
}

// FileRange 对应 Kotlin 的 Pair<String, Int>
type FileRange struct {
	URL    string
	Offset int
}

// FromBytes 对应 Kotlin 的 companion object fun fromBytes
// 逻辑：Gzip 解压 -> JSON 解析
func FromBytes(data []byte) (*MixFile, error) {
	// 1. Gzip 解压
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	// 2. JSON 解析
	var mixFile MixFile
	if err := json.Unmarshal(decompressed, &mixFile); err != nil {
		return nil, err
	}

	return &mixFile, nil
}

// GetFileListByStartRange 获取从指定偏移量开始的文件列表
func (m *MixFile) GetFileListByStartRange(startRange int64) []FileRange {
	startIndex := int(startRange / int64(m.ChunkSize))
	startOffset := int(startRange % int64(m.ChunkSize))

	if startIndex >= len(m.FileList) {
		return []FileRange{}
	}

	// 截取从 startIndex 开始的列表
	subList := m.FileList[startIndex:]
	result := make([]FileRange, len(subList))

	for i, file := range subList {
		offset := 0
		if i == 0 {
			offset = startOffset
		}
		result[i] = FileRange{
			URL:    file,
			Offset: offset,
		}
	}

	return result
}

// ToBytes 对应 Kotlin 的 fun toBytes
// 逻辑：JSON 序列化 -> Gzip 压缩
func (m *MixFile) ToBytes() ([]byte, error) {
	// 1. JSON 序列化
	jsonData, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	// 2. Gzip 压缩
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)

	if _, err := writer.Write(jsonData); err != nil {
		return nil, err
	}

	// 必须 Close 才能完成压缩刷入
	if err := writer.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
