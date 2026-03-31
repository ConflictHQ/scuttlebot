package api

import (
	"net/http"
	"time"
)

var startTime = time.Now()

type statusResponse struct {
	Status  string    `json:"status"`
	Uptime  string    `json:"uptime"`
	Agents  int       `json:"agents"`
	Started time.Time `json:"started"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	agents := s.registry.List()
	writeJSON(w, http.StatusOK, statusResponse{
		Status:  "ok",
		Uptime:  time.Since(startTime).Round(time.Second).String(),
		Agents:  len(agents),
		Started: startTime,
	})
}
