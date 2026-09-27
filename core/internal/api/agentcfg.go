package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/config"
)

const agentConfigMax = maxConfigYAML

type agentConfigResponse struct {
	Path      string         `json:"path"`
	YAML      string         `json:"yaml"`
	Writable  bool           `json:"writable"`
	Updated   int64          `json:"updated"`
	Size      int            `json:"size"`
	Backup    string         `json:"backup,omitempty"`
	Form      *config.Visual `json:"form,omitempty"`
	FormError string         `json:"form_error,omitempty"`
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
	s.configMu.Lock()
	defer s.configMu.Unlock()
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
		writeJSON(w, agentConfigView(s.opt.ConfigPath, yaml))
		return
	}
	var body struct {
		YAML      string         `json:"yaml"`
		Form      *config.Visual `json:"form"`
		IfUpdated *int64         `json:"if_updated"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, agentConfigMax+4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid config request", http.StatusBadRequest)
		return
	}
	if body.Form != nil && strings.TrimSpace(body.YAML) != "" {
		http.Error(w, "send yaml or form, not both", http.StatusBadRequest)
		return
	}
	if body.IfUpdated != nil && *body.IfUpdated != configFileMTime(s.opt.ConfigPath) {
		http.Error(w, "config changed since read", http.StatusConflict)
		return
	}
	yamlText := body.YAML
	if body.Form != nil {
		current, err := readAgentConfig(s.opt.ConfigPath)
		if err != nil {
			http.Error(w, "read agent config failed", http.StatusInternalServerError)
			return
		}
		yamlText, err = config.ApplyVisual(current.YAML, *body.Form)
		if err != nil {
			http.Error(w, "invalid config: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := writeAgentConfigIfUpdated(s.opt.ConfigPath, yamlText, body.IfUpdated); err != nil {
		if errors.Is(err, config.ErrFileConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		var ve *configValidateError
		if errors.As(err, &ve) {
			http.Error(w, ve.Error(), http.StatusBadRequest)
			return
		}
		s.log.Error("write agent config", "err", err)
		http.Error(w, "write agent config failed", http.StatusInternalServerError)
		return
	}
	written, err := readAgentConfig(s.opt.ConfigPath)
	if err != nil {
		http.Error(w, "read agent config failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, agentConfigView(s.opt.ConfigPath, written))
}

func (s *Server) handleManageConfigRollback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.allowLocalAgentManage(w, r) {
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.opt.ConfigPath == "" {
		http.Error(w, "agent config file is not configured", http.StatusNotFound)
		return
	}
	bak, err := os.ReadFile(s.opt.ConfigPath + ".bak")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "config backup is missing", http.StatusNotFound)
			return
		}
		s.log.Error("read agent config backup", "err", err)
		http.Error(w, "read agent config backup failed", http.StatusInternalServerError)
		return
	}
	if err := writeAgentConfig(s.opt.ConfigPath, string(bak)); err != nil {
		var ve *configValidateError
		if errors.As(err, &ve) {
			http.Error(w, ve.Error(), http.StatusBadRequest)
			return
		}
		s.log.Error("restore agent config", "err", err)
		http.Error(w, "restore agent config failed", http.StatusInternalServerError)
		return
	}
	written, err := readAgentConfig(s.opt.ConfigPath)
	if err != nil {
		http.Error(w, "read agent config failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, agentConfigView(s.opt.ConfigPath, written))
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
	if s.opt.ConfigPath != "" {
		b, err := os.ReadFile(s.opt.ConfigPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Error("read agent config before restart", "err", err)
			http.Error(w, "read agent config failed", http.StatusInternalServerError)
			return
		}
		if err == nil {
			if _, err := config.Parse(b); err != nil {
				http.Error(w, "config on disk will not load", http.StatusConflict)
				return
			}
		}
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"status": "restarting", "ok": true})
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

type agentConfigSnapshot struct {
	YAML    string
	Updated int64
}

func readAgentConfig(path string) (agentConfigSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return agentConfigSnapshot{}, nil
		}
		return agentConfigSnapshot{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, agentConfigMax+1))
	if err != nil {
		return agentConfigSnapshot{}, err
	}
	if len(b) > agentConfigMax {
		return agentConfigSnapshot{}, errors.New("config file is too large")
	}
	// Local saves and remote applies replace the path atomically. Stat the
	// opened file so a concurrent replacement cannot pair old YAML with its
	// newer revision and incorrectly authorize a stale conditional save.
	info, err := f.Stat()
	if err != nil {
		return agentConfigSnapshot{}, err
	}
	return agentConfigSnapshot{YAML: string(b), Updated: info.ModTime().UnixMicro()}, nil
}

type configValidateError struct{ err error }

func (e *configValidateError) Error() string { return "invalid config: " + e.err.Error() }
func (e *configValidateError) Unwrap() error { return e.err }

func writeAgentConfig(path, yaml string) error {
	return writeAgentConfigIfUpdated(path, yaml, nil)
}

func writeAgentConfigIfUpdated(path, yaml string, expected *int64) error {
	if strings.TrimSpace(yaml) == "" {
		return &configValidateError{err: errors.New("yaml is empty")}
	}
	if len(yaml) > agentConfigMax {
		return &configValidateError{err: errors.New("yaml is too large")}
	}
	if _, err := config.Parse([]byte(yaml)); err != nil {
		return &configValidateError{err: err}
	}
	return config.SaveIfUpdated(path, []byte(yaml), expected)
}

// configFileMTime returns Unix microseconds: detect edits within the same second
// while retaining exact integer precision in browser clients. Zero means absent.
func configFileMTime(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime().UnixMicro()
	}
	return 0
}

func agentConfigView(path string, snapshot agentConfigSnapshot) agentConfigResponse {
	resp := agentConfigResponse{
		Path: path, YAML: snapshot.YAML, Writable: path != "",
		Updated: snapshot.Updated, Size: len(snapshot.YAML),
	}
	if path != "" {
		if _, err := os.Stat(path + ".bak"); err == nil {
			resp.Backup = path + ".bak"
		}
	}
	form, err := config.VisualFrom(snapshot.YAML)
	if err != nil {
		resp.FormError = "配置里有表单无法展示的内容，请用 YAML 修改"
		return resp
	}
	resp.Form = &form
	return resp
}
