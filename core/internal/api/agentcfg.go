package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/config"
)

const agentConfigMax = 1 << 20

type agentConfigResponse struct {
	Path     string `json:"path"`
	YAML     string `json:"yaml"`
	Writable bool   `json:"writable"`
}

func (s *Server) allowLocalAgentManage(w http.ResponseWriter, r *http.Request) bool {
	if node := r.URL.Query().Get("node"); node != "" && node != "local" {
		http.Error(w, "agent config is local only", http.StatusBadRequest)
		return false
	}
	u := userOf(r)
	if u.Role != RoleAdmin || u.principal == "" {
		http.Error(w, "agent config requires an authenticated admin", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) handleManageConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.allowLocalAgentManage(w, r) {
		return
	}
	if s.opt.ConfigPath == "" {
		if r.Method == http.MethodGet {
			writeJSON(w, agentConfigResponse{})
			return
		}
		http.Error(w, "agent config file is not configured", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		yaml, err := readAgentConfig(s.opt.ConfigPath)
		if err != nil {
			http.Error(w, "read agent config failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, agentConfigResponse{Path: s.opt.ConfigPath, YAML: yaml, Writable: true})
		return
	}
	var body struct {
		YAML string `json:"yaml"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, agentConfigMax+4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid config request", http.StatusBadRequest)
		return
	}
	if err := writeAgentConfig(s.opt.ConfigPath, body.YAML); err != nil {
		var ve *configValidateError
		if errors.As(err, &ve) {
			http.Error(w, ve.Error(), http.StatusBadRequest)
			return
		}
		s.log.Error("write agent config", "err", err)
		http.Error(w, "write agent config failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, agentConfigResponse{Path: s.opt.ConfigPath, YAML: body.YAML, Writable: true})
}

func (s *Server) handleManageRestart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.allowLocalAgentManage(w, r) {
		return
	}
	if s.opt.RequestRestart == nil {
		http.Error(w, "restart is not available", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"status": "restarting"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	restart := s.opt.RequestRestart
	log := s.log
	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := restart(); err != nil {
			log.Error("agent restart", "err", err)
		}
	}()
}

func readAgentConfig(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if len(b) > agentConfigMax {
		return "", errors.New("config file is too large")
	}
	return string(b), nil
}

type configValidateError struct{ err error }

func (e *configValidateError) Error() string { return "invalid config: " + e.err.Error() }
func (e *configValidateError) Unwrap() error { return e.err }

func writeAgentConfig(path, yaml string) error {
	if strings.TrimSpace(yaml) == "" {
		return &configValidateError{err: errors.New("yaml is empty")}
	}
	if len(yaml) > agentConfigMax {
		return &configValidateError{err: errors.New("yaml is too large")}
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".monitor-config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteString(yaml); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := config.Load(tmp); err != nil {
		return &configValidateError{err: err}
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
