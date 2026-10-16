package server

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/coder/websocket"

	"github.com/neotoxicfr/dockerr/internal/auth"
	"github.com/neotoxicfr/dockerr/internal/execterm"
)

func (s *Server) execAllowed() bool { return s.cfg.ExecEnabled && s.docker.Caps.Exec }

func (s *Server) terminalPage(w http.ResponseWriter, r *http.Request) {
	if !s.advancedOnly(w, r) {
		return
	}
	c, _, ok := s.findContainer(w, r)
	if !ok {
		return
	}
	s.render(w, r, "terminal", c.Name, "resources", map[string]any{"C": c, "Enabled": s.execAllowed(), "ConfigOn": s.cfg.ExecEnabled})
}

func (s *Server) execSocket(w http.ResponseWriter, r *http.Request) {
	if !s.advancedOnly(w, r) {
		return
	}
	if !s.execAllowed() {
		http.Error(w, "terminal désactivé", http.StatusForbidden)
		return
	}
	c, _, ok := s.findContainer(w, r)
	if !ok {
		return
	}
	if !c.Running() {
		http.Error(w, "conteneur arrêté", http.StatusConflict)
		return
	}
	// Accept checks that Origin matches Host: no cross-site WebSocket.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	cols, _ := strconv.ParseUint(r.URL.Query().Get("cols"), 10, 16)
	rows, _ := strconv.ParseUint(r.URL.Query().Get("rows"), 10, 16)
	user := auth.User(r.Context())
	id, _ := s.store.ExecStart(r.Context(), user, c.Name, execterm.Shell[len(execterm.Shell)-1])
	slog.Info("exec start", "user", user, "container", c.Name)
	err = execterm.Serve(r.Context(), ws, s.docker.API, c.ID, r.URL.Query().Get("user"), uint(max(cols, 20)), uint(max(rows, 5)))
	s.store.ExecEnd(context.Background(), id)
	slog.Info("exec end", "user", user, "container", c.Name, "err", err)
	if err != nil {
		_ = ws.Close(websocket.StatusInternalError, err.Error())
	}
}
