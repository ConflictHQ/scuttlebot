package roommcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Remote uses the hosted native MCP service, retaining its server-side identity
// and acknowledgment state rather than creating a second local mailbox.
type Remote struct{ client *mcp.ClientSession }
type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}
func ConnectRemote(ctx context.Context, baseURL, token string) (*Remote, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "scuttlebot-native-inbox", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             strings.TrimRight(baseURL, "/") + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerTransport{token}, Timeout: 25 * time.Second},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, err
	}
	return &Remote{client: session}, nil
}
func (r *Remote) Close() error { return r.client.Close() }
func (r *Remote) call(ctx context.Context, name string, args, out any) error {
	res, err := r.client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("hosted room tool %s failed", name)
	}
	if out == nil {
		return nil
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
func (r *Remote) Open(ctx context.Context, in SessionInput) (OpenResult, error) {
	var out OpenResult
	err := r.call(ctx, "open_session", in, &out)
	return out, err
}
func (r *Remote) Read(ctx context.Context, in SessionInput) (ReadResult, error) {
	var out ReadResult
	err := r.call(ctx, "read_messages", ReadInput{SessionInput: in, Limit: 100, AddressedOnly: true}, &out)
	return out, err
}
func (r *Remote) Ack(ctx context.Context, in SessionInput, cursor string) error {
	return r.call(ctx, "ack_messages", AckInput{SessionInput: in, Cursor: cursor}, nil)
}
func (r *Remote) Send(ctx context.Context, in SessionInput, text string) error {
	return r.call(ctx, "send_message", SendInput{SessionInput: in, Text: text}, nil)
}
