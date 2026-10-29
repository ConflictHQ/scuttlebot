package api

import (
	"fmt"
	"net/http"

	"github.com/conflicthq/scuttlebot/internal/roommcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) nativeMCPHandler(apiHandler http.Handler) http.Handler {
	service := &roommcp.Service{Store: s.roomStore, Resolve: func(req *mcp.CallToolRequest) (roommcp.Access, error) {
		if req.Extra == nil {
			return roommcp.Access{}, fmt.Errorf("authentication required")
		}
		token := bearerToken(&http.Request{Header: req.Extra.Header})
		key := s.apiKeys.Lookup(token)
		if key == nil {
			return roommcp.Access{}, fmt.Errorf("authentication required")
		}
		return roommcp.Access{Owner: key.ID, Backend: &roommcp.HTTPBackend{
			URL: "http://scuttlebot.internal", Token: token,
			Client: &http.Client{Transport: roommcp.HandlerTransport{Handler: apiHandler}},
		}}, nil
	}}
	server := service.Server()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.NewCrossOriginProtection().Handler(s.authMiddleware(handler))
}
