package hostfiles

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const maxTransfer = 128 << 20
const stagePrefix = ".monitor-upload-"

// Keep one in-progress upload per host. Its directory handle stays anchored
// even if another process moves the directory while chunks are in flight.
type upload struct {
	id, name, temporary string
	dir                 *os.Root
	file                *os.File
	size, written       int64
	timer               *time.Timer
}

func (s *Store) clearUpload() {
	u := s.upload
	u.timer.Stop()
	u.file.Close()
	u.dir.Remove(u.temporary)
	u.dir.Close()
	s.upload = nil
}
func (s *Store) transfer(root *os.Root, action, path string, args map[string]string) (json.RawMessage, error) {
	if action == "read-chunk" {
		off, err := strconv.ParseInt(args["offset"], 10, 64)
		if err != nil || off < 0 {
			return nil, errors.New("无效偏移")
		}
		st, err := root.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() || st.Size() > maxTransfer || off > st.Size() {
			return nil, errors.New("不支持的文件类型或大小")
		}
		f, err := root.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		actual, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !os.SameFile(st, actual) {
			return nil, errors.New("文件已变化，请重新下载")
		}
		revision := fmt.Sprintf("%d/%d", actual.Size(), actual.ModTime().UnixNano())
		if expected := args["revision"]; expected != "" && expected != revision {
			return nil, errors.New("文件已变化，请重新下载")
		}
		data, err := io.ReadAll(io.NewSectionReader(f, off, MaxBytes))
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"data": data, "size": actual.Size(), "revision": revision})
	}
	if action == "upload-start" {
		if s.upload != nil {
			return nil, errors.New("此主机有上传任务正在进行")
		}
		size, err := strconv.ParseInt(args["size"], 10, 64)
		if err != nil || size <= MaxBytes || size > maxTransfer {
			return nil, errors.New("文件大小超出范围")
		}
		dir, err := root.OpenRoot(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		name := filepath.Base(path)
		if _, err = dir.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			dir.Close()
			return nil, errors.New("目标已存在或无法访问")
		}
		id := rand.Text()
		temporary := stagePrefix + rand.Text()
		file, err := dir.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			dir.Close()
			return nil, err
		}
		u := &upload{id: id, name: name, temporary: temporary, dir: dir, file: file, size: size}
		s.upload = u
		u.timer = time.AfterFunc(2*time.Minute, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.upload == u {
				s.clearUpload()
			}
		})
		return json.Marshal(id)
	}
	u := s.upload
	if u == nil || args["id"] != u.id {
		return nil, errors.New("上传任务已结束，请重新上传")
	}
	switch action {
	case "upload-cancel":
		s.clearUpload()
	case "upload-chunk":
		offset, err := strconv.ParseInt(args["offset"], 10, 64)
		if err != nil || offset != u.written {
			return nil, errors.New("上传进度不匹配")
		}
		data, err := base64.StdEncoding.DecodeString(args["data"])
		if err != nil || len(data) == 0 || len(data) > MaxBytes || offset+int64(len(data)) > u.size {
			return nil, errors.New("无效文件块")
		}
		if _, err = u.file.Write(data); err != nil {
			s.clearUpload()
			return nil, err
		}
		u.written += int64(len(data))
		u.timer.Reset(2 * time.Minute)
	case "upload-finish":
		if u.written != u.size {
			return nil, errors.New("上传尚未完成")
		}
		defer s.clearUpload()
		if err := u.file.Close(); err != nil {
			return nil, err
		}
		// Publish only the complete file; Link cannot replace an existing name.
		if err := u.dir.Link(u.temporary, u.name); err != nil {
			return nil, err
		}
	}
	return json.RawMessage("null"), nil
}
