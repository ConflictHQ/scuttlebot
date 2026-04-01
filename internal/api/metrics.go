package api

import (
	"net/http"
	"runtime"
	"time"
)

type metricsResponse struct {
	Timestamp string          `json:"timestamp"`
	Runtime   runtimeMetrics  `json:"runtime"`
	Bridge    *bridgeMetrics  `json:"bridge,omitempty"`
	Registry  registryMetrics `json:"registry"`
}

type runtimeMetrics struct {
	Goroutines int    `json:"goroutines"`
	HeapAlloc  uint64 `json:"heap_alloc_bytes"`
	HeapSys    uint64 `json:"heap_sys_bytes"`
	GCRuns     uint32 `json:"gc_runs"`
}

type bridgeMetrics struct {
	Channels      int   `json:"channels"`
	MessagesTotal int64 `json:"messages_total"`
	ActiveSubs    int   `json:"active_subscribers"`
}

type registryMetrics struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Revoked int `json:"revoked"`
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	resp := metricsResponse{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Runtime: runtimeMetrics{
			Goroutines: runtime.NumGoroutine(),
			HeapAlloc:  ms.HeapAlloc,
			HeapSys:    ms.HeapSys,
			GCRuns:     ms.NumGC,
		},
	}

	if s.bridge != nil {
		st := s.bridge.Stats()
		resp.Bridge = &bridgeMetrics{
			Channels:      st.Channels,
			MessagesTotal: st.MessagesTotal,
			ActiveSubs:    st.ActiveSubs,
		}
	}

	agents := s.registry.List()
	for _, a := range agents {
		resp.Registry.Total++
		if a.Revoked {
			resp.Registry.Revoked++
		} else {
			resp.Registry.Active++
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
