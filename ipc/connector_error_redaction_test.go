package ipc

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestConnectorErrorRedaction(t *testing.T) {
	const secret = "private-credential"
	upstream := server.NewMCPServer("probe", "1")
	upstream.AddTool(mcp.NewTool("fail"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("credential rejected: " + secret)
	})
	httpServer := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	defer httpServer.Close()
	tool := ConnectorTool{Connector: &ConnectorSpec{Name: "probe", URL: httpServer.URL, CallTimeout: time.Second}, RemoteName: "fail"}
	_, err := CallConnectorTool(context.Background(), tool, nil, map[string]string{"TOKEN": secret})
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "«redacted»") {
		t.Errorf("connector error = %v", err)
	}
	tool.Connector.URL = "http://%" + secret
	_, err = CallConnectorTool(context.Background(), tool, nil, map[string]string{"TOKEN": secret})
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "«redacted»") {
		t.Errorf("connector initialization error = %v", err)
	}
}
