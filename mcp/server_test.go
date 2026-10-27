package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolsThroughSDKClient(t *testing.T) {
	server := NewServer("test",
		func(context.Context) ([]Endpoint, error) {
			return []Endpoint{{Name: "edge-a", Port: 12001, User: "root", Description: "test endpoint"}}, nil
		},
		func(context.Context) ([]Tunnel, error) {
			return []Tunnel{{Name: "edge-a", Port: 12001, Status: "UP", Uptime: "00:10:00"}}, nil
		},
	)
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 2 || listed.Tools[0].Name != "get_tunnel_status" || listed.Tools[1].Name != "list_endpoints" {
		t.Fatalf("unexpected tool list: %#v", listed.Tools)
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint || !tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil {
			t.Errorf("tool %q lacks read-only annotations: %#v", tool.Name, tool.Annotations)
		}
		if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Title == "" {
			t.Errorf("tool %q lacks schema or title", tool.Name)
		}
	}

	endpoints, err := clientSession.CallTool(ctx, &sdk.CallToolParams{Name: "list_endpoints"})
	if err != nil {
		t.Fatal(err)
	}
	if endpoints.IsError || len(endpoints.Content) != 1 || endpoints.StructuredContent == nil {
		t.Fatalf("endpoint result missing structured and text content: %#v", endpoints)
	}
	tunnels, err := clientSession.CallTool(ctx, &sdk.CallToolParams{Name: "get_tunnel_status"})
	if err != nil {
		t.Fatal(err)
	}
	if tunnels.IsError || len(tunnels.Content) != 1 || tunnels.StructuredContent == nil {
		t.Fatalf("tunnel result missing structured and text content: %#v", tunnels)
	}
	if _, err := clientSession.CallTool(ctx, &sdk.CallToolParams{Name: "invite"}); err == nil {
		t.Fatal("write tool was exposed")
	}
}

func TestHTTPHandlerRejectsUnauthorizedRequests(t *testing.T) {
	cases := []struct {
		name, authorization, origin string
		want                        int
	}{
		{"no token", "", "", 401},
		{"wrong token", "Bearer wrong", "", 401},
		{"token without Bearer", "secret", "", 401},
		{"untrusted origin", "Bearer secret", "https://untrusted.example", 403},
	}
	handler := HTTPHandler(NewServer("test", nil, nil), "secret")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rejected := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "http://127.0.0.1:8421/mcp", nil)
			if c.authorization != "" {
				request.Header.Set("Authorization", c.authorization)
			}
			if c.origin != "" {
				request.Header.Set("Origin", c.origin)
			}
			handler.ServeHTTP(rejected, request)
			if rejected.Code != c.want {
				t.Fatalf("status = %d, want %d", rejected.Code, c.want)
			}
		})
	}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestHTTPHandlerServesSDKClientWithToken(t *testing.T) {
	server := NewServer("test", nil, nil)
	httpServer := httptest.NewServer(HTTPHandler(server, "secret"))
	defer httpServer.Close()
	ctx := context.Background()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	connect := func(token string) (*sdk.ClientSession, error) {
		return client.Connect(ctx, &sdk.StreamableClientTransport{
			Endpoint:             httpServer.URL,
			HTTPClient:           &http.Client{Transport: bearer{token}},
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}, nil)
	}
	if session, err := connect("wrong"); err == nil {
		session.Close()
		t.Fatal("connected with a wrong token")
	}
	session, err := connect("secret")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 2 {
		t.Fatalf("unexpected tool list: %#v", listed.Tools)
	}
}
