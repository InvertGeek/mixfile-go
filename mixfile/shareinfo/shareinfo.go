package shareinfo

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mixfile-go/mixfile/aes"
	"mixfile-go/mixfile/basen"
	"net/http"
	"net/url"
	"strings"
)

var password = md5.Sum([]byte("123"))

func DecodeMixShareInfo(code string) ([]byte, error) {
	decoded := basen.Decode(code)
	decrypted, err := aes.DecryptAES(decoded, password[:])
	if err != nil {
		return nil, err
	}
	return decrypted, nil
}

// MixShareInfo 对应 Kotlin 的 MixShareInfo
type MixShareInfo struct {
	FileName   string `json:"f"`
	FileSize   int64  `json:"s"`
	HeadSize   int    `json:"h"`
	URL        string `json:"u"`
	Key        string `json:"k"`
	Referer    string `json:"r"` // 对应 Kotlin 的 @SerialName("r")
	CachedCode string `json:"-"` // Transient，不参与 JSON 序列化
}

// 从分享码解析
func FromString(code string) (*MixShareInfo, error) {
	shareInfoBytes, err := DecodeMixShareInfo(code)
	if err != nil {
		return nil, err
	}
	var info MixShareInfo
	if err := json.Unmarshal(shareInfoBytes, &info); err != nil {
		return nil, err
	}
	info.CachedCode = code
	return &info, nil
}

func (m *MixShareInfo) DoFetchFile(
	client *http.Client,
	targetURL string,
	referer string,
) ([]byte, error) {
	var limit = 1024 * 1024 * 20
	// 2. 构建请求
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	// 3. 执行请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 4. 校验文件大小
	contentLength := resp.ContentLength
	// 严格对标: limit + headSize + 24 (IV 12 + Tag 12)
	maxSize := int64(limit + m.HeadSize + 24)
	if contentLength > maxSize {
		overSize := contentLength - maxSize
		return nil, fmt.Errorf("分片文件过大: %d bytes", overSize)
	}

	// 5. 跳过头部 (Kotlin: channel.discard)
	if m.HeadSize > 0 {
		_, err = io.CopyN(io.Discard, resp.Body, int64(m.HeadSize))
		if err != nil {
			return nil, fmt.Errorf("discard head error: %v", err)
		}
	}

	decodedKey := basen.Decode(m.Key)

	// 调用之前严格对标 12字节Tag 的流解密函数
	result, err := aes.DecryptAESStream(resp.Body, decodedKey, limit)
	if err != nil {
		return nil, err
	}

	// 7. SHA256 校验 (Kotlin: Url(url).fragment)
	u, err := url.Parse(targetURL)
	if err == nil && u.Fragment != "" {
		expectedHash := strings.TrimSpace(u.Fragment)

		// 计算结果的 SHA256
		hasher := sha256.New()
		hasher.Write(result)
		currentHash := basen.Encode(hasher.Sum(nil))

		// 比较 Hash (不区分大小写)
		if !strings.EqualFold(currentHash, expectedHash) {
			return nil, fmt.Errorf("文件遭到篡改: %s != %s", currentHash, expectedHash)
		}
	}

	return result, nil
}

// 辅助函数：获取 URL 里的 # 后面的内容
func getFragmentFromURL(rawURL string) string {
	parts := strings.Split(rawURL, "#")
	if len(parts) > 1 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}
