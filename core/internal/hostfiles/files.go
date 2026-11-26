// Package hostfiles serves a single configured directory on the owning host.
package hostfiles

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const MaxBytes = 512 << 10

type Entry struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}
type Store struct {
	Dir    string
	Create bool
	mu     sync.Mutex
	upload *upload
}

func valid(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, stagePrefix) {
			return false
		}
	}
	return fs.ValidPath(path) && !strings.ContainsAny(path, "\\:\x00") && filepath.IsLocal(filepath.FromSlash(path))
}
func (s *Store) Run(action string, args map[string]string) (json.RawMessage, error) {
	if s == nil {
		return nil, errors.New("此主机未启用文件管理")
	}
	if !s.mu.TryLock() {
		return nil, errors.New("文件操作进行中，请重试")
	}
	defer s.mu.Unlock()
	if s.Dir == "" {
		return nil, errors.New("此主机未配置文件目录")
	}
	if s.Create {
		if err := os.MkdirAll(s.Dir, 0700); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	path := args["path"]
	if !valid(path) || path == "." && action != "list" {
		return nil, errors.New("无效路径")
	}
	path = filepath.FromSlash(path)
	switch action {
	case "read-chunk", "upload-start", "upload-chunk", "upload-finish", "upload-cancel":
		return s.transfer(root, action, path, args)
	case "list":
		st, err := root.Stat(path)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			return nil, errors.New("请选择目录")
		}
		dir, err := root.Open(path)
		if err != nil {
			return nil, err
		}
		defer dir.Close()
		items, err := dir.ReadDir(4097)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(items) > 4096 {
			return nil, errors.New("目录超过 4096 项，请选择较小的管理目录")
		}
		entries := make([]Entry, 0, len(items))
		for _, item := range items {
			if strings.HasPrefix(item.Name(), stagePrefix) {
				continue
			}
			info, err := item.Info()
			if err != nil {
				continue
			}
			kind := "other"
			if info.IsDir() {
				kind = "folder"
			} else if info.Mode().IsRegular() {
				kind = "file"
			}
			entries = append(entries, Entry{info.Name(), kind, info.Size(), info.ModTime().Unix()})
		}
		return json.Marshal(entries)
	case "read":
		info, err := root.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > MaxBytes {
			return nil, errors.New("仅支持 512 KiB 以内的普通文件")
		}
		f, err := root.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > MaxBytes {
			return nil, errors.New("文件超过 512 KiB")
		}
		return json.Marshal(data)
	case "mkdir":
		err = root.Mkdir(path, 0750)
	case "upload":
		data, e := base64.StdEncoding.DecodeString(args["data"])
		if e != nil || len(data) > MaxBytes {
			return nil, errors.New("上传无效或超过 512 KiB")
		}
		f, e := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, err = f.Write(data)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = root.Remove(path)
		}
	case "rename":
		name := args["name"]
		if !valid(name) || name == "." || strings.Contains(name, "/") {
			return nil, errors.New("无效名称")
		}
		dst := filepath.Join(filepath.Dir(path), name)
		if _, e := root.Lstat(dst); !errors.Is(e, os.ErrNotExist) {
			return nil, errors.New("目标已存在或无法访问")
		}
		err = root.Rename(path, dst)
	case "delete":
		err = root.Remove(path)
	default:
		return nil, errors.New("未知文件操作")
	}
	return json.RawMessage("null"), err
}
