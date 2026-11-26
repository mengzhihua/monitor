package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !shellPersonalAdmin(userOf(r)) || r.URL.Query().Has("token") {
		http.Error(w, "文件管理需要个人管理员账户", 403)
		return
	}
	if !s.shellSecure(r) || !s.shellOrigin(r) {
		http.Error(w, "请通过同源 HTTPS 或本机回环地址访问文件管理", 403)
		return
	}
	action := r.PathValue("action")
	switch action {
	case "list", "read", "mkdir", "upload", "rename", "delete", "read-chunk", "upload-start", "upload-chunk", "upload-finish", "upload-cancel":
	default:
		http.NotFound(w, r)
		return
	}
	var args map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 750<<10)).Decode(&args); err != nil {
		http.Error(w, "无效文件请求", 400)
		return
	}
	var result json.RawMessage
	var err error
	node := r.URL.Query().Get("node")
	if node == "" {
		result, err = s.opt.Files.Run(action, args)
	} else {
		if s.opt.Nodes == nil {
			http.Error(w, "未找到主机", 404)
			return
		}
		nd, ok := s.opt.Nodes.Get(node)
		if !ok || nd.IsReplica() {
			http.Error(w, "请在主机接入的 Hub 打开文件管理", 409)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		result, err = nd.Files(ctx, action, args)
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, result)
}
