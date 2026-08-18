package notify

import (
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type clientFrame struct {
	Op           string   `json:"op"`
	Topics       []string `json:"topics,omitempty"`
	EndpointHash string   `json:"endpoint_hash,omitempty"`
}

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 60 * time.Second
	wsPingPeriod = 30 * time.Second
	wsMaxMsgSize = 4096
)

// HandleWebSocket upgrades the request and serves the notify protocol.
func HandleWebSocket(c *gin.Context) {
	if !Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "notifications disabled"})
		return
	}
	conn := &wsConn{send: make(chan []byte, 64), topics: make(map[string]struct{}), wildcards: make(map[string]struct{})}
	if !globalHub.register(conn) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "too many connections"})
		return
	}
	ws, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		globalHub.unregister(conn)
		return
	}

	// Writer: owns all writes to the socket.
	go func() {
		ticker := time.NewTicker(wsPingPeriod)
		defer func() {
			ticker.Stop()
			_ = ws.Close()
		}()
		for {
			select {
			case payload, ok := <-conn.send:
				_ = ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
				if !ok {
					_ = ws.WriteMessage(websocket.CloseMessage, []byte{})
					return
				}
				if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
					return
				}
			case <-ticker.C:
				_ = ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
				if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	sendFrame := func(v interface{}) {
		payload, err := common.Marshal(v)
		if err != nil {
			return
		}
		_ = conn.trySend(payload)
	}

	sendFrame(map[string]interface{}{"op": "hello", "protocol": ProtocolVersion, "server_time": time.Now().Unix()})

	// Reader loop.
	ws.SetReadLimit(wsMaxMsgSize)
	_ = ws.SetReadDeadline(time.Now().Add(wsPongWait))
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(wsPongWait))
		RefreshPresence(conn.endpointHash)
		return nil
	})
	defer func() {
		ClearPresence(conn.endpointHash)
		globalHub.unregister(conn)
		_ = ws.Close()
	}()
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var frame clientFrame
		if err := common.Unmarshal(raw, &frame); err != nil {
			sendFrame(map[string]interface{}{"op": "error", "message": "bad frame"})
			continue
		}
		switch frame.Op {
		case "subscribe":
			topics := SanitizeTopics(frame.Topics, maxTopicsPerConn)
			if len(topics) == 0 && len(frame.Topics) > 0 {
				sendFrame(map[string]interface{}{"op": "error", "message": "invalid topics"})
				continue
			}
			if frame.EndpointHash != "" && ValidTopic(frame.EndpointHash) {
				conn.endpointHash = frame.EndpointHash
				RefreshPresence(conn.endpointHash)
			}
			current := conn.setTopics(topics, true)
			sendFrame(map[string]interface{}{"op": "subscribed", "topics": current})
		case "unsubscribe":
			current := conn.setTopics(SanitizeTopics(frame.Topics, maxTopicsPerConn), false)
			sendFrame(map[string]interface{}{"op": "subscribed", "topics": current})
		case "ping":
			RefreshPresence(conn.endpointHash)
			sendFrame(map[string]interface{}{"op": "pong"})
		default:
			sendFrame(map[string]interface{}{"op": "error", "message": "unknown op"})
		}
	}
}
