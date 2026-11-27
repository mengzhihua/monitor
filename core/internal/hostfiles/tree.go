package hostfiles

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxTreeBytes = 32 << 20
	maxTreeFiles = 1024
)

type treeBudget struct {
	files int
	bytes int64
}

func (s *Store) relocate(root *os.Root, action, src, destDir string) error {
	if destDir == "" {
		destDir = "."
	}
	if !valid(destDir) {
		return errors.New("无效路径")
	}
	info, err := root.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("不支持符号链接")
	}
	if nested(filepath.ToSlash(src), destDir) {
		return errors.New("不能把目录放进自身")
	}
	name := filepath.Base(src)
	if action == "copy" {
		name, err = availableName(root, destDir, name)
		if err != nil {
			return err
		}
	}
	dst := joinSlash(destDir, name)
	if !valid(dst) {
		return errors.New("无效路径")
	}
	target := filepath.FromSlash(dst)
	if action == "move" {
		if _, err = root.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			return errors.New("目标已存在或无法访问")
		}
		return root.Rename(src, target)
	}
	var budget treeBudget
	if err = copyEntry(root, src, target, info, &budget); err != nil {
		_ = removeTree(root, target)
		return err
	}
	return nil
}

func (s *Store) archive(root *os.Root, dir, name, rawNames string) error {
	dir = filepath.ToSlash(dir)
	if dir == "" {
		dir = "."
	}
	if !valid(dir) {
		return errors.New("无效路径")
	}
	var names []string
	if err := json.Unmarshal([]byte(rawNames), &names); err != nil || len(names) == 0 || len(names) > 256 {
		return errors.New("请选择要压缩的项目")
	}
	if name == "" {
		name = "归档.zip"
	}
	if !valid(name) || strings.Contains(name, "/") || !strings.HasSuffix(strings.ToLower(name), ".zip") {
		return errors.New("无效名称")
	}
	name, err := availableName(root, dir, name)
	if err != nil {
		return err
	}
	dst := filepath.FromSlash(joinSlash(dir, name))
	file, err := root.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(file)
	var budget treeBudget
	writeErr := error(nil)
	for _, item := range names {
		if !valid(item) || strings.Contains(item, "/") {
			writeErr = errors.New("无效名称")
			break
		}
		src := filepath.FromSlash(joinSlash(dir, item))
		info, err := root.Lstat(src)
		if err != nil {
			writeErr = err
			break
		}
		if info.Mode()&os.ModeSymlink != 0 {
			writeErr = errors.New("不支持符号链接")
			break
		}
		if writeErr = addZip(root, zw, src, item, info, &budget); writeErr != nil {
			break
		}
	}
	closeErr := zw.Close()
	fileErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		writeErr = fileErr
	}
	if writeErr != nil {
		_ = root.Remove(dst)
	}
	return writeErr
}

func (s *Store) extract(root *os.Root, src, destDir string) error {
	if destDir == "" {
		destDir = "."
	}
	if !valid(destDir) {
		return errors.New("无效路径")
	}
	info, err := root.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxTreeBytes {
		return errors.New("压缩包超出范围")
	}
	file, err := root.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return errors.New("无法读取压缩包")
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	folderName, err := availableName(root, destDir, base)
	if err != nil {
		return err
	}
	folder := filepath.FromSlash(joinSlash(destDir, folderName))
	if err = root.Mkdir(folder, 0750); err != nil {
		return err
	}
	var budget treeBudget
	for _, item := range reader.File {
		if err = unzipOne(root, folder, item, &budget); err != nil {
			_ = removeTree(root, folder)
			return err
		}
	}
	return nil
}

func unzipOne(root *os.Root, folder string, item *zip.File, budget *treeBudget) error {
	raw := item.Name
	if raw == "" || strings.Contains(raw, `\`) || strings.Contains(raw, "..") || path.IsAbs(raw) {
		return errors.New("压缩包包含无效路径")
	}
	name := path.Clean(raw)
	if name == "." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
		return errors.New("压缩包包含无效路径")
	}
	if !valid(name) && name != "." {
		return errors.New("压缩包包含无效路径")
	}
	target := filepath.Join(folder, filepath.FromSlash(name))
	if item.FileInfo().IsDir() {
		return root.MkdirAll(target, 0750)
	}
	if !item.FileInfo().Mode().IsRegular() || item.FileInfo().Mode()&os.ModeSymlink != 0 {
		return errors.New("压缩包包含不支持的项目")
	}
	if item.UncompressedSize64 > uint64(maxTreeBytes) || budget.bytes+int64(item.UncompressedSize64) > maxTreeBytes {
		return errors.New("解压超过 32 MiB")
	}
	budget.files++
	if budget.files > maxTreeFiles {
		return errors.New("项目过多")
	}
	if err := root.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return err
	}
	out, err := root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	in, err := item.Open()
	if err != nil {
		out.Close()
		return err
	}
	written, err := io.Copy(out, io.LimitReader(in, maxTreeBytes+1))
	in.Close()
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil && written > maxTreeBytes {
		err = errors.New("解压超过 32 MiB")
	}
	budget.bytes += written
	return err
}

func addZip(root *os.Root, zw *zip.Writer, src, name string, info os.FileInfo, budget *treeBudget) error {
	if info.IsDir() {
		entries, err := readEntries(root, src)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			_, err = zw.Create(name + "/")
			return err
		}
		for _, entry := range entries {
			child, err := entry.Info()
			if err != nil {
				return err
			}
			if child.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if err = addZip(root, zw, filepath.Join(src, entry.Name()), name+"/"+entry.Name(), child, budget); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("仅支持普通文件和目录")
	}
	if err := spend(budget, info.Size()); err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.Method = zip.Deflate
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	file, err := root.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(writer, io.LimitReader(file, info.Size()))
	return err
}

func copyEntry(root *os.Root, src, dst string, info os.FileInfo, budget *treeBudget) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("不支持符号链接")
	}
	if info.IsDir() {
		if err := root.Mkdir(dst, 0750); err != nil {
			return err
		}
		entries, err := readEntries(root, src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			child, err := entry.Info()
			if err != nil {
				return err
			}
			if child.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if err = copyEntry(root, filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), child, budget); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("仅支持普通文件和目录")
	}
	if err := spend(budget, info.Size()); err != nil {
		return err
	}
	in, err := root.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := root.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(in, info.Size()))
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func spend(budget *treeBudget, size int64) error {
	if size < 0 || budget.bytes+size > maxTreeBytes {
		return errors.New("超过 32 MiB")
	}
	budget.files++
	if budget.files > maxTreeFiles {
		return errors.New("项目过多")
	}
	budget.bytes += size
	return nil
}

func availableName(root *os.Root, dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	candidate := name
	for i := 0; i < 20; i++ {
		full := filepath.FromSlash(joinSlash(dir, candidate))
		_, err := root.Lstat(full)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", errors.New("目标已存在或无法访问")
		}
		if i == 0 {
			candidate = stem + " 副本" + ext
		} else {
			candidate = fmt.Sprintf("%s 副本 %d%s", stem, i+1, ext)
		}
	}
	return "", errors.New("目标已存在或无法访问")
}

func removeTree(root *os.Root, target string) error {
	info, err := root.Lstat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := readEntries(root, target)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err = removeTree(root, filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
	}
	return root.Remove(target)
}

func readEntries(root *os.Root, dir string) ([]os.DirEntry, error) {
	file, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.ReadDir(-1)
}

func joinSlash(dir, name string) string {
	if dir == "" || dir == "." {
		return name
	}
	return dir + "/" + name
}

func nested(src, destDir string) bool {
	src = path.Clean(src)
	destDir = path.Clean(destDir)
	return src != "." && (destDir == src || strings.HasPrefix(destDir, src+"/"))
}
