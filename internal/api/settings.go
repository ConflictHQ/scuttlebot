package api

import "net/http"

type settingsResponse struct {
	TLS      tlsInfo  `json:"tls"`
	Policies Policies `json:"policies"`
}

type tlsInfo struct {
	Enabled       bool   `json:"enabled"`
	Domain        string `json:"domain,omitempty"`
	AllowInsecure bool   `json:"allow_insecure"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	resp := settingsResponse{
		TLS: tlsInfo{
			Enabled:       s.tlsDomain != "",
			Domain:        s.tlsDomain,
			AllowInsecure: true, // always true in current build
		},
	}
	if s.policies != nil {
		resp.Policies = s.policies.Get()
	}
	writeJSON(w, http.StatusOK, resp)
}
