package mcp

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	HTTPAddress = "127.0.0.1:8421"
	HTTPPath    = "/mcp"
)

type Endpoint struct {
	Name        string `json:"name"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	Description string `json:"description,omitempty"`
}

type Tunnel struct {
	Name        string `json:"name"`
	Port        int    `json:"port"`
	Status      string `json:"status"`
	Uptime      string `json:"uptime"`
	Description string `json:"description,omitempty"`
}

type EndpointList struct {
	Endpoints []Endpoint `json:"endpoints"`
}

type TunnelList struct {
	Tunnels []Tunnel `json:"tunnels"`
}

type ReadEndpoints func(context.Context) ([]Endpoint, error)
type ReadTunnels func(context.Context) ([]Tunnel, error)

func NewServer(version string, readEndpoints ReadEndpoints, readTunnels ReadTunnels) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "bastionhub", Version: version}, nil)
	readOnly := false
	closedWorld := false
	sdk.AddTool(server, &sdk.Tool{
		Name: "list_endpoints", Title: "List endpoints",
		Description: "List the endpoints registered in the local bastionhub configuration.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &readOnly, IdempotentHint: true, OpenWorldHint: &closedWorld, Title: "List endpoints"},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, EndpointList, error) {
		endpoints, err := readEndpoints(ctx)
		if err != nil {
			return nil, EndpointList{}, err
		}
		return nil, EndpointList{Endpoints: endpoints}, nil
	})
	openWorld := true
	sdk.AddTool(server, &sdk.Tool{
		Name: "get_tunnel_status", Title: "Get tunnel status",
		Description: "Read the current tunnel listener status from the configured bastion.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &readOnly, IdempotentHint: true, OpenWorldHint: &openWorld, Title: "Get tunnel status"},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, TunnelList, error) {
		tunnels, err := readTunnels(ctx)
		if err != nil {
			return nil, TunnelList{}, err
		}
		return nil, TunnelList{Tunnels: tunnels}, nil
	})
	return server
}

// HTTPHandler serves the server over Streamable HTTP. A request whose
// Authorization header is not "Bearer <token>" answers 401; a cross-origin
// browser request answers 403.
func HTTPHandler(server *sdk.Server, token string) http.Handler {
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true})
	authorized := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
	return http.NewCrossOriginProtection().Handler(authorized)
}
