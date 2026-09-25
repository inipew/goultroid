package download

import (
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/services/storage"
)

const (
	extractorResultPrefix   = "goultroid-result:"
	extractorResultTemplate = "after_move:" + extractorResultPrefix + "%(filepath)j\t%(title)j\t%(artist)j\t%(uploader)j\t%(duration)j\t%(width)j\t%(height)j\t%(ext)j"
)

type extractorResultMetadata struct {
	Path     string
	Title    string
	Artist   string
	Uploader string
	Duration float64
	Width    int
	Height   int
	Ext      string
}

func parseExtractorResult(stdout string) extractorResultMetadata {
	var result extractorResultMetadata
	for _, line := range strings.Split(strings.ReplaceAll(stdout, "\r", "\n"), "\n") {
		line = strings.TrimSpace(line)
		index := strings.Index(line, extractorResultPrefix)
		if index < 0 {
			continue
		}
		fields := strings.Split(line[index+len(extractorResultPrefix):], "\t")
		if len(fields) < 8 {
			continue
		}
		result = extractorResultMetadata{
			Path:     decodeExtractorJSONString(fields[0]),
			Title:    decodeExtractorJSONString(fields[1]),
			Artist:   decodeExtractorJSONString(fields[2]),
			Uploader: decodeExtractorJSONString(fields[3]),
			Duration: decodeExtractorJSONFloat(fields[4]),
			Width:    int(decodeExtractorJSONFloat(fields[5])),
			Height:   int(decodeExtractorJSONFloat(fields[6])),
			Ext:      decodeExtractorJSONString(fields[7]),
		}
	}
	return result
}

func decodeExtractorJSONString(raw string) string {
	raw = strings.TrimSpace(raw)
	var value string
	if err := json.Unmarshal([]byte(raw), &value); err == nil {
		if strings.EqualFold(value, "NA") || strings.EqualFold(value, "none") {
			return ""
		}
		return strings.TrimSpace(value)
	}
	return ""
}

func decodeExtractorJSONFloat(raw string) float64 {
	raw = strings.TrimSpace(raw)
	var value float64
	if err := json.Unmarshal([]byte(raw), &value); err == nil && value > 0 {
		return value
	}
	return 0
}

func resolveExtractorOutput(tmpDir, reportedPath string) (string, error) {
	if candidate, ok := safeExtractorOutput(tmpDir, reportedPath); ok {
		return candidate, nil
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", fmt.Errorf("failed to scan downloaded files: %w", err)
	}
	var (
		targetPath string
		targetSize int64 = -1
	)
	for _, entry := range entries {
		if entry.IsDir() || extractorSidecar(entry.Name()) {
			continue
		}
		path := filepath.Join(tmpDir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.Size() > targetSize {
			targetPath = path
			targetSize = info.Size()
		}
	}
	if targetPath == "" {
		return "", fmt.Errorf("%w: no final media file produced by extractor", ErrDownloadFailed)
	}
	return targetPath, nil
}

func safeExtractorOutput(tmpDir, reportedPath string) (string, bool) {
	reportedPath = strings.TrimSpace(reportedPath)
	if reportedPath == "" {
		return "", false
	}
	path := reportedPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(tmpDir, path)
	}
	rootAbs, err := filepath.Abs(tmpDir)
	if err != nil {
		return "", false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", false
	}
	info, err := os.Lstat(pathAbs)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	return pathAbs, true
}

func extractorSidecar(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range []string{
		".part", ".ytdl", ".json", ".description",
		".vtt", ".srt", ".ass", ".lrc",
		".jpg", ".jpeg", ".png", ".webp",
	} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func extractorMIME(path string, mode MediaMode) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".opus", ".ogg":
		return "audio/ogg"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		if mode == MediaModeAudio {
			return "audio/webm"
		}
		return "video/webm"
	}
	if detected := mime.TypeByExtension(ext); detected != "" {
		if semi := strings.IndexByte(detected, ';'); semi >= 0 {
			detected = detected[:semi]
		}
		return strings.TrimSpace(detected)
	}
	return ""
}

func extractorStorageMetadata(path string, result extractorResultMetadata, mode MediaMode) storage.Metadata {
	title := truncateSearchText(result.Title, extractorSearchTitleBytes)
	if title == "" {
		title = truncateSearchText(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), extractorSearchTitleBytes)
	}
	performer := truncateSearchText(result.Artist, extractorSearchChannelBytes)
	if performer == "" {
		performer = truncateSearchText(result.Uploader, extractorSearchChannelBytes)
	}
	var duration time.Duration
	if result.Duration > 0 {
		duration = time.Duration(result.Duration * float64(time.Second))
	}
	return storage.Metadata{
		Name:      filepath.Base(path),
		MIME:      extractorMIME(path, mode),
		Title:     title,
		Performer: performer,
		Duration:  duration,
		Width:     result.Width,
		Height:    result.Height,
	}
}
