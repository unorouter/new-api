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

	// Room relay. Topic fans out to the room; ConnID addresses one guest.
	// MsgID and Meta drive storage so a guest who reloads gets the transcript.
	Topic  string `json:"topic,omitempty"`
	ConnID string `json:"conn_id,omitempty"`
	Data   string `json:"data,omitempty"`
	MsgID  string `json:"msg_id,omitempty"`
	Meta   bool   `json:"meta,omitempty"`
	Close  bool   `json:"close,omitempty"`
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
	conn := &wsConn{
		id:        common.GetUUID(),
		send:      make(chan []byte, 64),
		topics:    make(map[string]struct{}),
		wildcards: make(map[string]struct{}),
	}
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

	// conn_id is how a room host addresses one guest. It is assigned here, never
	// supplied by the client, so it cannot be used to impersonate another guest.
	sendFrame(map[string]interface{}{
		"op":          "hello",
		"protocol":    ProtocolVersion,
		"server_time": time.Now().Unix(),
		"conn_id":     conn.id,
	})

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
	// Per-connection floor between room frames. There is no inbound rate limit
	// on this endpoint otherwise, and a room turn is typed by a human.
	var lastRoomFrame time.Time
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
			// A room id is the only thing protecting a room's contents, and
			// ValidTopic would otherwise accept `room:*`, which matches every
			// room on the platform. Drop any room subscription that is not an
			// exact id.
			topics = rejectRoomWildcards(topics)
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
		case "room":
			if !ValidRoomTopic(frame.Topic) {
				sendFrame(map[string]interface{}{"op": "error", "message": "invalid room"})
				continue
			}
			// Only a member may relay into a room, so holding the id is not
			// enough to speak into one you never joined.
			if !conn.matches([]string{frame.Topic}) {
				sendFrame(map[string]interface{}{"op": "error", "message": "not in room"})
				continue
			}
			now := time.Now()
			if now.Sub(lastRoomFrame) < roomMinFrameGap {
				sendFrame(map[string]interface{}{"op": "error", "message": "too fast"})
				continue
			}
			lastRoomFrame = now
			if frame.Close {
				DeleteRoom(frame.Topic)
				continue
			}
			if frame.Meta {
				SetRoomMeta(frame.Topic, frame.Data)
			} else if frame.MsgID != "" {
				AppendRoomMessage(frame.Topic, frame.MsgID, frame.Data)
			}
			PublishRoomFrame(RoomFrame{
				Topic:  frame.Topic,
				ConnID: frame.ConnID,
				Data:   frame.Data,
			})
		case "ping":
			RefreshPresence(conn.endpointHash)
			sendFrame(map[string]interface{}{"op": "pong"})
		default:
			sendFrame(map[string]interface{}{"op": "error", "message": "unknown op"})
		}
	}
}
