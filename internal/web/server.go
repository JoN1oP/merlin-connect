// Package web serves the UI and its JSON API on localhost.
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"merlin-connect/internal/app"
	"merlin-connect/internal/library"
	"merlin-connect/internal/media"
)

//go:embed static
var static embed.FS

// Server is the HTTP handler for the UI and API.
type Server struct {
	session *app.Session
	lib     *library.Library
	mux     *http.ServeMux

	mu     sync.Mutex
	pages  int       // open event streams (browser tabs)
	opened bool      // a page connected at least once
	idle   time.Time // when the last page closed
}

// New wires the routes.
func New(session *app.Session, lib *library.Library) *Server {
	s := &Server{session: session, lib: lib, mux: http.NewServeMux()}
	assets, _ := fs.Sub(static, "static")
	s.mux.Handle("GET /", http.FileServerFS(assets))
	s.mux.HandleFunc("GET /api/state", s.state)
	s.mux.HandleFunc("GET /api/events", s.events)
	s.mux.HandleFunc("GET /covers/{uuid}", s.cover)
	s.mux.HandleFunc("POST /api/connect", s.action(session.Connect))
	s.mux.HandleFunc("POST /api/cancel", s.action(session.Cancel))
	s.mux.HandleFunc("POST /api/disconnect", s.action(session.Disconnect))
	s.mux.HandleFunc("POST /api/sync", s.sync)
	s.mux.HandleFunc("POST /api/folders", s.addFolder)
	s.mux.HandleFunc("POST /api/import", s.importFiles)
	s.mux.HandleFunc("PATCH /api/items/{uuid}", s.editItem)
	s.mux.HandleFunc("PUT /api/items/{uuid}/cover", s.setCover)
	s.mux.HandleFunc("DELETE /api/items/{uuid}", s.deleteItem)
	return s
}

// ServeHTTP only answers localhost, and requires the X-Merlin header on
// changes so other web pages cannot drive the API.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Header.Get("X-Merlin") != "1" {
		http.Error(w, "missing X-Merlin header", http.StatusForbidden)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// Idle reports how long no page has been open, once one has been. It is zero
// while a page is open or before the first page connects.
func (s *Server) Idle() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opened || s.pages > 0 {
		return 0
	}
	return time.Since(s.idle)
}

type snapshot struct {
	Status app.Status `json:"status"`
	Tree   *app.Node  `json:"tree"`
}

func (s *Server) snapshot() snapshot {
	return snapshot{Status: s.session.Status(), Tree: s.session.View()}
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshot())
}

// events streams a snapshot after every change (server-sent events).
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.pages++
	s.opened = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.pages--
		s.idle = time.Now()
		s.mu.Unlock()
	}()

	changes, stop := s.session.Subscribe()
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		data, _ := json.Marshal(s.snapshot())
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-changes:
		case <-heartbeat.C:
		}
	}
}

func (s *Server) cover(w http.ResponseWriter, r *http.Request) {
	data, err := s.session.Cover(r.PathValue("uuid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=31536000, immutable") // URLs carry ?v=
	w.Write(data)
}

func (s *Server) action(fn func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fn()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	if err := s.session.Sync(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addFolder(w http.ResponseWriter, r *http.Request) {
	var req struct{ Parent, Title string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.lib.AddFolder(req.Parent, req.Title, nil)
	s.done(w, item, err)
}

// importFiles receives multipart parts in pairs: a "path" field with the
// file's relative path (folder imports keep their structure), then the "file".
func (s *Server) importFiles(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tmp, err := os.MkdirTemp("", "merlin-import-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer os.RemoveAll(tmp)

	var rel string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		switch part.FormName() {
		case "path":
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			rel = filepath.FromSlash(string(b))
		case "file":
			if !filepath.IsLocal(rel) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("bad path %q", rel))
				return
			}
			if err := saveTo(filepath.Join(tmp, rel), part); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			rel = ""
		}
	}

	fsys := os.DirFS(tmp)
	root, err := media.Scan(fsys)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.lib.Import(r.URL.Query().Get("parent"), root, fsys)
	s.done(w, res, err)
}

func saveTo(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (s *Server) editItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    *string
		Parent   *string
		Position *int
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	uuid := r.PathValue("uuid")
	var err error
	if req.Title != nil {
		err = s.lib.Rename(uuid, *req.Title)
	}
	if err == nil && (req.Parent != nil || req.Position != nil) {
		item, ok := s.lib.Item(uuid)
		if !ok {
			err = library.ErrNotFound
		} else {
			parent, position := item.Parent, item.Position
			if req.Parent != nil {
				parent, position = *req.Parent, math.MaxInt // end of the new folder
			}
			if req.Position != nil {
				position = *req.Position
			}
			err = s.lib.Move(uuid, parent, position)
		}
	}
	s.done(w, nil, err)
}

func (s *Server) setCover(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err == nil {
		err = s.lib.SetCover(r.PathValue("uuid"), data)
	}
	s.done(w, nil, err)
}

func (s *Server) deleteItem(w http.ResponseWriter, r *http.Request) {
	s.done(w, nil, s.lib.Delete(r.PathValue("uuid")))
}

// done answers a library change and tells the session the library moved.
func (s *Server) done(w http.ResponseWriter, result any, err error) {
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.session.LibraryChanged()
	if result == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": app.Message(err)})
}
