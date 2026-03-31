package api

import (
	"embed"
	"net/http"
)

//go:embed ui/*
var uiFS embed.FS

func (s *Server) uiFileServer() http.Handler {
	return http.FileServerFS(uiFS)
}
