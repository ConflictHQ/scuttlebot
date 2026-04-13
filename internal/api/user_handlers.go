package api

import (
	"encoding/json"
	"net/http"

	"github.com/conflicthq/scuttlebot/pkg/passgen"
)

// handleUserSetPassword handles PUT /v1/users/{nick}/password.
// Resets the NickServ password for an IRC user account.
func (s *Server) handleUserSetPassword(w http.ResponseWriter, r *http.Request) {
	nick := r.PathValue("nick")

	var req struct {
		Password string `json:"password"` // explicit; empty = generate
		Length   int    `json:"length"`   // for generation (default 24)
		Charset  string `json:"charset"`  // for generation (default alphanum)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	password := req.Password
	if password == "" {
		opts := &passgen.Options{}
		if req.Length > 0 {
			opts.Length = req.Length
		}
		if req.Charset != "" {
			cs, err := passgen.ParseCharset(req.Charset)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			opts.Charset = cs
		}
		var err error
		password, err = passgen.Generate(opts)
		if err != nil {
			s.log.Error("generate password", "err", err)
			writeError(w, http.StatusInternalServerError, "password generation failed")
			return
		}
	}

	if err := s.ircPasswd.ChangePassword(nick, password); err != nil {
		s.log.Error("set user password", "nick", nick, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to set password: "+err.Error())
		return
	}

	// If a matching admin account exists, keep it in sync.
	adminSynced := false
	if s.admins != nil {
		if err := s.admins.SetPassword(nick, password); err == nil {
			adminSynced = true
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"nick":         nick,
		"password":     password,
		"admin_synced": adminSynced,
	})
}
