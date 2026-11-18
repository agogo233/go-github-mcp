// Package transport starts the MCP server over stdio or Streamable HTTP.
package transport

import (
	"fmt"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// RunStdio serves MCP over stdin/stdout. Logs must never touch stdout:
// the MCP protocol owns that stream.
func RunStdio(s *mcpserver.MCPServer) error {
	if err := mcpserver.ServeStdio(s); err != nil {
		return fmt.Errorf("stdio server: %w", err)
	}
	return nil
}
