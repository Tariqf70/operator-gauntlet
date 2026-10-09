// Package fakecloud implements the cloud database API from
// specs/manageddatabase/CLOUD_API.md, plus hooks the harness uses to inject
// crashes and inspect state. It has no dependencies outside the standard library.
package fakecloud

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status values.
const (
	StatusCreating  = "creating"
	StatusAvailable = "available"
	StatusResizing  = "resizing"
	StatusDeleting  = "deleting"
)

var nameRE = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

// Database is the public view of a database (no password).
type Database struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Engine   string `json:"engine"`
	SizeGB   int    `json:"sizeGB"`
	Status   string `json:"status"`
	Endpoint string `json:"endpoint"`
	Username string `json:"username"`
}

type record struct {
	Database
	password   string
	transition time.Time // when the current transitional status ends
	createdAt  time.Time
}

// Stats counts API calls, for reports.
type Stats struct {
	CreateCalls       int `json:"createCalls"`
	CreateConflicts   int `json:"createConflicts"`
	DeleteCalls       int `json:"deleteCalls"`
	PatchCalls        int `json:"patchCalls"`
	ResetPasswordCall int `json:"resetPasswordCalls"`
	ListCalls         int `json:"listCalls"`
	GetCalls          int `json:"getCalls"`
}

// Server is an in-memory fake cloud. The zero value is not usable; call New.
type Server struct {
	// Timing knobs. Change them only before serving or between test phases.
	CreateDuration time.Duration // creating -> available
	ResizeDuration time.Duration // resizing -> available
	DeleteDuration time.Duration // deleting -> gone
	Latency        time.Duration // added to every request
	CreateHold     time.Duration // delay between storing a create and responding (keeps the call in flight)

	// OnCreate, if set, runs synchronously after a create is stored and before the
	// response is written. The harness uses it to SIGKILL the manager at a
	// deterministic crash point.
	OnCreate func(Database)

	mu      sync.Mutex
	now     func() time.Time
	byID    map[string]*record
	byName  map[string]string // name -> id (only live databases)
	stats   Stats
	deleted []Database // databases that finished deleting, for reports
}

// New returns a server with short default timings suitable for tests.
func New() *Server {
	return &Server{
		CreateDuration: 3 * time.Second,
		ResizeDuration: 2 * time.Second,
		DeleteDuration: 2 * time.Second,
		now:            time.Now,
		byID:           map[string]*record{},
		byName:         map[string]string{},
	}
}

// SetClock replaces the time source (tests only).
func (s *Server) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// advance applies time-based status transitions. Caller holds s.mu.
func (s *Server) advance() {
	now := s.now()
	for id, r := range s.byID {
		if r.transition.IsZero() || now.Before(r.transition) {
			continue
		}
		switch r.Status {
		case StatusCreating, StatusResizing:
			r.Status = StatusAvailable
			r.transition = time.Time{}
		case StatusDeleting:
			s.deleted = append(s.deleted, r.Database)
			delete(s.byName, r.Name)
			delete(s.byID, id)
		}
	}
}

// ---- Harness-facing API --------------------------------------------------

// SetOnCreate sets (or clears, with nil) the create hook while the server is running.
func (s *Server) SetOnCreate(f func(Database)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.OnCreate = f
}

// SetCreateHold sets the create hold while the server is running.
func (s *Server) SetCreateHold(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CreateHold = d
}

// List returns all live databases (including ones still deleting), sorted by name.
func (s *Server) List() []Database {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	out := make([]Database, 0, len(s.byID))
	for _, r := range s.byID {
		out = append(out, r.Database)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Deleted returns databases that have finished deleting.
func (s *Server) Deleted() []Database {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance()
	return append([]Database(nil), s.deleted...)
}

// Verify reports whether password is the current password of database id.
func (s *Server) Verify(id, password string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	return ok && password != "" && r.password == password
}

// Stats returns a copy of the call counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Reset clears all state and counters (between test phases).
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID = map[string]*record{}
	s.byName = map[string]string{}
	s.deleted = nil
	s.stats = Stats{}
}

// ---- HTTP API ------------------------------------------------------------

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, apiError{Error: kind, Message: msg})
}

type createReq struct {
	Name   string `json:"name"`
	Engine string `json:"engine"`
	SizeGB int    `json:"sizeGB"`
}

type withPassword struct {
	Database
	Password string `json:"password"`
}

func validateCreate(req createReq) error {
	if !nameRE.MatchString(req.Name) {
		return errors.New("name must match ^[a-z0-9-]{1,63}$")
	}
	if req.Engine != "postgres" && req.Engine != "mysql" {
		return errors.New("engine must be postgres or mysql")
	}
	if req.SizeGB < 10 || req.SizeGB > 1000 {
		return errors.New("sizeGB must be between 10 and 1000")
	}
	return nil
}

// ServeHTTP implements the public API plus /_admin endpoints.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/v1/databases" && r.Method == http.MethodPost:
		s.handleCreate(w, r)
	case path == "/v1/databases" && r.Method == http.MethodGet:
		s.handleList(w, r)
	case strings.HasPrefix(path, "/v1/databases/"):
		rest := strings.TrimPrefix(path, "/v1/databases/")
		if id, ok := strings.CutSuffix(rest, "/reset-password"); ok && r.Method == http.MethodPost {
			s.handleResetPassword(w, id)
			return
		}
		if strings.Contains(rest, "/") {
			writeErr(w, http.StatusNotFound, "NotFound", "no such route")
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.handleGet(w, rest)
		case http.MethodPatch:
			s.handlePatch(w, r, rest)
		case http.MethodDelete:
			s.handleDelete(w, rest)
		default:
			writeErr(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
		}
	case path == "/_admin/databases" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"items": s.List(), "deleted": s.Deleted(), "stats": s.Stats()})
	case path == "/_admin/verify" && r.Method == http.MethodPost:
		var v struct{ ID, Password string }
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			writeErr(w, http.StatusBadRequest, "BadRequest", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"valid": s.Verify(v.ID, v.Password)})
	case path == "/_admin/reset" && r.Method == http.MethodPost:
		s.Reset()
		w.WriteHeader(http.StatusNoContent)
	default:
		writeErr(w, http.StatusNotFound, "NotFound", "no such route")
	}
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	if err := validateCreate(req); err != nil {
		writeErr(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	s.mu.Lock()
	s.advance()
	s.stats.CreateCalls++
	if _, exists := s.byName[req.Name]; exists {
		s.stats.CreateConflicts++
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "AlreadyExists", "a database named "+req.Name+" already exists")
		return
	}
	now := s.now()
	rec := &record{
		Database: Database{
			ID:       "db-" + randomHex(6),
			Name:     req.Name,
			Engine:   req.Engine,
			SizeGB:   req.SizeGB,
			Status:   StatusCreating,
			Endpoint: req.Name + ".db.fakecloud.local:" + map[string]string{"postgres": "5432", "mysql": "3306"}[req.Engine],
			Username: "admin",
		},
		password:   randomHex(16),
		transition: now.Add(s.CreateDuration),
		createdAt:  now,
	}
	s.byID[rec.ID] = rec
	s.byName[rec.Name] = rec.ID
	resp := withPassword{Database: rec.Database, Password: rec.password}
	hook, hold := s.OnCreate, s.CreateHold
	s.mu.Unlock()

	// Crash point: the database now exists, but the caller hasn't seen the response.
	if hook != nil {
		hook(resp.Database)
	}
	if hold > 0 {
		time.Sleep(hold)
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	s.mu.Lock()
	s.advance()
	s.stats.ListCalls++
	items := []Database{}
	if name != "" {
		if id, ok := s.byName[name]; ok {
			items = append(items, s.byID[id].Database)
		}
	} else {
		for _, rec := range s.byID {
			items = append(items, rec.Database)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGet(w http.ResponseWriter, id string) {
	s.mu.Lock()
	s.advance()
	s.stats.GetCalls++
	rec, ok := s.byID[id]
	var db Database
	if ok {
		db = rec.Database
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "NotFound", "no database "+id)
		return
	}
	writeJSON(w, http.StatusOK, db)
}

func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		SizeGB int `json:"sizeGB"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	if req.SizeGB < 10 || req.SizeGB > 1000 {
		writeErr(w, http.StatusBadRequest, "BadRequest", "sizeGB must be between 10 and 1000")
		return
	}
	s.mu.Lock()
	s.advance()
	s.stats.PatchCalls++
	rec, ok := s.byID[id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "NotFound", "no database "+id)
		return
	}
	if rec.Status != StatusAvailable {
		st := rec.Status
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "NotAvailable", "database is "+st)
		return
	}
	if rec.SizeGB != req.SizeGB {
		rec.SizeGB = req.SizeGB
		rec.Status = StatusResizing
		rec.transition = s.now().Add(s.ResizeDuration)
	}
	db := rec.Database
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, db)
}

func (s *Server) handleDelete(w http.ResponseWriter, id string) {
	s.mu.Lock()
	s.advance()
	s.stats.DeleteCalls++
	rec, ok := s.byID[id]
	if ok && rec.Status != StatusDeleting {
		rec.Status = StatusDeleting
		rec.transition = s.now().Add(s.DeleteDuration)
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "NotFound", "no database "+id)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleResetPassword(w http.ResponseWriter, id string) {
	s.mu.Lock()
	s.advance()
	s.stats.ResetPasswordCall++
	rec, ok := s.byID[id]
	var pw string
	if ok {
		rec.password = randomHex(16)
		pw = rec.password
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "NotFound", "no database "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"password": pw})
}
