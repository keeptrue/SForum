package extensionpackage

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// ErrInvalidArchive 表示 ZIP 结构、条目或资源边界不符合扩展包约束。
var ErrInvalidArchive = errors.New("extension package: invalid archive")

// ArchiveLimits 约束中央目录条目数和实际解压后的总字节数。
type ArchiveLimits struct {
	Entries int
	Bytes   int64
}

// ReadArchive 在内存和路径边界内解析上传包，不执行或落盘任何包内容。
func ReadArchive(data []byte, limits ArchiveLimits) (extensionmanifest.Manifest, []File, error) {
	if limits.Entries <= 0 || limits.Bytes <= 0 {
		return extensionmanifest.Manifest{}, nil, ErrInvalidArchive
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) > limits.Entries {
		return extensionmanifest.Manifest{}, nil, ErrInvalidArchive
	}

	var rootBody []byte
	files := make([]File, 0, len(reader.File))
	fileMap := extensionmanifest.FileMapFS{}
	seen := map[string]struct{}{}
	var total int64
	for _, file := range reader.File {
		// 在不可信 ZIP 条目边界执行可见的严格校验；除目录穿越外，也拒绝
		// Windows 盘符/UNC 路径。包含 ".." 的普通文件名同样不属于扩展包格式。
		if strings.Contains(file.Name, "..") {
			return extensionmanifest.Manifest{}, nil, ErrInvalidArchive
		}
		rawName := strings.TrimSpace(strings.ReplaceAll(file.Name, "\\", "/"))
		windowsDrivePath := len(rawName) >= 2 && rawName[1] == ':' &&
			((rawName[0] >= 'A' && rawName[0] <= 'Z') || (rawName[0] >= 'a' && rawName[0] <= 'z'))
		if rawName == "" || strings.ContainsRune(rawName, '\x00') || strings.HasPrefix(rawName, "/") || windowsDrivePath {
			return extensionmanifest.Manifest{}, nil, ErrInvalidArchive
		}
		name, ok := extensionmanifest.SafeArchivePath(rawName)
		if !ok || file.Mode()&os.ModeSymlink != 0 {
			return extensionmanifest.Manifest{}, nil, ErrInvalidArchive
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return extensionmanifest.Manifest{}, nil, ErrInvalidArchive
		}
		seen[name] = struct{}{}

		body, err := readZipFileLimited(file, limits.Bytes-total)
		if err != nil {
			return extensionmanifest.Manifest{}, nil, ErrInvalidArchive
		}
		total += int64(len(body))
		fileMap[name] = body
		if name == extensionmanifest.ManifestFileName {
			rootBody = body
			continue
		}
		files = append(files, File{Path: name, Mode: file.Mode(), Body: body})
	}
	if rootBody == nil {
		return extensionmanifest.Manifest{}, nil, ErrInvalidArchive
	}
	manifest, err := extensionmanifest.LoadRootBytes(rootBody, fileMap)
	if err != nil {
		return extensionmanifest.Manifest{}, nil, ErrInvalidManifest
	}
	return manifest, files, nil
}

func readZipFileLimited(file *zip.File, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, ErrInvalidArchive
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrInvalidArchive
	}
	return body, nil
}
