package outbound

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

// Good: internal/outbound may dial WebSocket connections.
func DialWebSocket(ctx context.Context, client *http.Client, target string) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: client})
	return conn, err
}
