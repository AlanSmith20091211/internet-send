// Package server implements the LAN hub: a small HTTP server that devices on
// the same network open in a browser to exchange files and text.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/skip2/go-qrcode"

	"github.com/AlanSmith20091211/internet-send/web"
)

const maxTextBytes = 1 << 20

type Config struct {
	Dir      string // where received files are stored
	PIN      string // optional access PIN; empty disables auth
	Version  string
	Hostname string
	URLs     []string // LAN addresses other devices can use to reach the hub
}

type Server struct {
	cfg  Config
	hub  *Hub
	auth *auth
	mux  *http.ServeMux
}

func New(cfg Config) (*Server, error) {
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, hub: newHub(), auth: newAuth(cfg.PIN), mux: http.NewServeMux()}

	s.mux.HandleFunc("GET /api/info", s.handleInfo)
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.Handle("GET /api/events", s.protect(s.handleEvents))
	s.mux.Handle("POST /api/upload", s.protect(s.handleUpload))
	s.mux.Handle("POST /api/text", s.protect(s.handleText))
	s.mux.Handle("GET /api/items/{id}/file", s.protect(s.handleDownload))
	s.mux.Handle("GET /api/qr", s.protect(s.handleQR))
	s.mux.HandleFunc("GET /", s.handleStatic(http.FileServerFS(web.FS)))
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

// Close disconnects all live event streams so the HTTP server can shut down.
func (s *Server) Close() { s.hub.close() }

func (s *Server) protect(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.authorized(r) {
			httpError(w, http.StatusUnauthorized, "需要访问口令")
			return
		}
		h(w, r)
	})
}

// handleStatic serves the web UI. A "?pin=" query (used by the QR code) logs
// the browser in and redirects to a clean URL.
func (s *Server) handleStatic(files http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			httpError(w, http.StatusNotFound, "接口不存在")
			return
		}
		if pin := r.URL.Query().Get("pin"); pin != "" && r.URL.Path == "/" {
			if s.auth.login(w, r, pin) == nil {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	authed := s.auth.authorized(r)
	info := map[string]any{
		"name":        s.cfg.Hostname,
		"version":     s.cfg.Version,
		"pinRequired": s.cfg.PIN != "",
		"authorized":  authed,
	}
	if authed {
		info["urls"] = s.cfg.URLs
		// Only the hub's own machine gets the PIN back, so it can show a QR
		// code that logs other devices in directly.
		if s.cfg.PIN != "" && isLoopback(r) {
			info["pin"] = s.cfg.PIN
		}
	}
	writeJSON(w, info)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := s.auth.login(w, r, body.PIN); err != nil {
		code := http.StatusUnauthorized
		if errors.Is(err, errLocked) {
			code = http.StatusTooManyRequests
		}
		httpError(w, code, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("device")
	if !idPattern.MatchString(id) {
		httpError(w, http.StatusBadRequest, "无效的设备 ID")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, "不支持流式响应")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	name := cleanLabel(q.Get("name"), 40)
	if name == "" {
		name = "未命名设备"
	}
	c, hi := s.hub.join(Device{ID: id, Name: name, Platform: cleanLabel(q.Get("platform"), 20)})
	defer s.hub.leave(c)

	data, _ := json.Marshal(hi)
	writeEvent(w, event{"hello", data})
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-c.ch:
			if !ok {
				return
			}
			writeEvent(w, ev)
			flusher.Flush()
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func writeEvent(w io.Writer, ev event) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data)
}

// resolveParties validates the sender and the optional recipient.
func (s *Server) resolveParties(fromID, toID string) (from, to Device, err error) {
	from, ok := s.hub.device(fromID)
	if !ok {
		return from, to, errors.New("当前设备未连接，请刷新页面")
	}
	if toID != "" {
		if to, ok = s.hub.device(toID); !ok {
			return from, to, errors.New("目标设备已离线")
		}
	}
	return from, to, nil
}

// handleUpload streams the raw request body to disk, so file size is bounded
// only by free space and memory use stays flat.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to, err := s.resolveParties(q.Get("device"), q.Get("to"))
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	name := sanitizeName(q.Get("name"))

	f, path, err := createUnique(s.cfg.Dir, name)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "无法保存文件："+err.Error())
		return
	}
	n, err := io.Copy(f, r.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && r.ContentLength >= 0 && n != r.ContentLength {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		os.Remove(path)
		httpError(w, http.StatusBadRequest, "传输中断："+err.Error())
		return
	}

	it := &Item{
		ID: newID(), Kind: "file",
		From: from.ID, FromName: from.Name, To: to.ID, ToName: to.Name,
		Name: name, Size: n, Mime: mime.TypeByExtension(filepath.Ext(name)),
		Time: time.Now(), path: path,
	}
	s.hub.addItem(it)
	writeJSON(w, it)
}

func (s *Server) handleText(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Device string `json:"device"`
		To     string `json:"to"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTextBytes)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "文字内容过长或格式错误")
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		httpError(w, http.StatusBadRequest, "内容为空")
		return
	}
	from, to, err := s.resolveParties(body.Device, body.To)
	if err != nil {
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	it := &Item{
		ID: newID(), Kind: "text",
		From: from.ID, FromName: from.Name, To: to.ID, ToName: to.Name,
		Text: body.Text, Time: time.Now(),
	}
	s.hub.addItem(it)
	writeJSON(w, it)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	it := s.hub.item(r.PathValue("id"))
	if it == nil || it.Kind != "file" || !it.visibleTo(r.URL.Query().Get("device")) {
		httpError(w, http.StatusNotFound, "文件不存在")
		return
	}
	f, err := os.Open(it.path)
	if err != nil {
		httpError(w, http.StatusGone, "文件已被移除")
		return
	}
	defer f.Close()

	// Uploaded content is untrusted: only media may render inline, and the
	// sandbox CSP stops anything else (HTML, SVG scripts) from running on
	// this origin.
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" && isPreviewable(it.Mime) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": it.Name}))
	w.Header().Set("Content-Security-Policy", "sandbox")
	http.ServeContent(w, r, it.Name, it.Time, f) // handles Range requests (resumable downloads)
}

func isPreviewable(m string) bool {
	return strings.HasPrefix(m, "image/") && m != "image/svg+xml" ||
		strings.HasPrefix(m, "video/") || strings.HasPrefix(m, "audio/")
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 512 {
		httpError(w, http.StatusBadRequest, "无效内容")
		return
	}
	png, err := qrcode.Encode(text, qrcode.Medium, 320)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}

// --- helpers ---

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func cleanLabel(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

var reservedWin = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sanitizeName turns a client-supplied file name into a safe single path
// component that is valid on Windows, macOS and Linux.
func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if !utf8.ValidString(name) {
		name = strings.ToValidUTF8(name, "_")
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	if name == "" {
		return "file"
	}
	ext := filepath.Ext(name)
	if reservedWin[strings.ToUpper(strings.TrimSuffix(name, ext))] {
		name = "_" + name
	}
	const maxLen = 200
	if len(name) > maxLen {
		if len(ext) > 20 {
			ext = ""
		}
		base := name[:maxLen-len(ext)]
		for !utf8.ValidString(base) {
			base = base[:len(base)-1]
		}
		name = base + ext
	}
	return name
}

// createUnique creates dir/name, or "name (1).ext", "name (2).ext", ... if
// the name is taken, without ever overwriting an existing file.
func createUnique(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 10000; i++ {
		cand := name
		if i > 0 {
			cand = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		p := filepath.Join(dir, cand)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, p, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("同名文件过多")
}
