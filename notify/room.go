package notify

import (
	"context"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	redisRoomChannel = "newapi:room:frames"
	redisRoomMsgsPfx = "newapi:room:msgs:"
	redisRoomMetaPfx = "newapi:room:meta:"

	// RoomTopicPrefix namespaces room subscriptions so a room id can never
	// collide with, or subscribe to, a notify event topic.
	RoomTopicPrefix = "room:"

	// A room outlives a reload but not a day. The host deleting the room is the
	// normal end; this is the backstop for a host who just closes the tab.
	roomTTL = 24 * time.Hour

	// A guest turn is human-paced. Anything faster is abuse, not usage.
	roomMinFrameGap = 200 * time.Millisecond
)

// RoomFrame is one relayed room message. Exactly one of Topic or ConnID is set:
// Topic fans out to everyone in the room, ConnID addresses a single guest.
type RoomFrame struct {
	Topic  string `json:"topic,omitempty"`
	ConnID string `json:"conn_id,omitempty"`
	// From is the sender's connection id, stamped by the server so the host can
	// tell guests apart and address a reply back. A client cannot forge it.
	From string `json:"from,omitempty"`
	Data string `json:"data"`
}

// ValidRoomTopic reports whether a topic may be used for a room.
//
// Wildcards are refused here even though ValidTopic allows one: `room:*` would
// otherwise be a legal subscription matching every room on the platform, and
// the room id is the only thing protecting a room's contents.
func ValidRoomTopic(topic string) bool {
	if !strings.HasPrefix(topic, RoomTopicPrefix) {
		return false
	}
	if strings.Contains(topic, "*") {
		return false
	}
	// A short or empty id is not a secret. Host ids are uid(24); the floor here
	// only has to rule out `room:` and other trivially guessable buckets.
	if len(strings.TrimPrefix(topic, RoomTopicPrefix)) < 16 {
		return false
	}
	return ValidTopic(topic)
}

// rejectRoomWildcards drops any room-namespaced pattern that is not an exact
// room id. Non-room topics pass through untouched.
func rejectRoomWildcards(topics []string) []string {
	out := make([]string, 0, len(topics))
	for _, t := range topics {
		if strings.HasPrefix(t, RoomTopicPrefix) && !ValidRoomTopic(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func roomKey(prefix string, topic string) string {
	return prefix + strings.TrimPrefix(topic, RoomTopicPrefix)
}

// PublishRoomFrame relays one frame to every replica, which each deliver it to
// their own local connections.
func PublishRoomFrame(frame RoomFrame) {
	if !Enabled() {
		return
	}
	payload, err := common.Marshal(frame)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := common.RDB.Publish(ctx, redisRoomChannel, payload).Err(); err != nil {
		common.SysError("notify: publish room frame failed: " + err.Error())
	}
}

// AppendRoomMessage stores one message so a guest who reloads gets the
// transcript back.
//
// msgID is the caller's message id and the row is REPLACED when it repeats: a
// stream delta carries the whole accumulated text for a message rather than an
// increment, so appending each one would store the same reply hundreds of
// times.
func AppendRoomMessage(topic string, msgID string, payload string) {
	if !Enabled() || msgID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := roomKey(redisRoomMsgsPfx, topic)

	if replaced := replaceRoomMessage(ctx, key, msgID, payload); !replaced {
		if err := common.RDB.RPush(ctx, key, payload).Err(); err != nil {
			common.SysError("notify: room append failed: " + err.Error())
			return
		}
	}
	// Refreshed on every write, so an active room never expires mid-session.
	common.RDB.Expire(ctx, key, roomTTL)
}

// replaceRoomMessage overwrites the stored row for msgID and reports whether it
// found one.
func replaceRoomMessage(ctx context.Context, key string, msgID string, payload string) bool {
	rows, err := common.RDB.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return false
	}
	needle := `"id":"` + msgID + `"`
	for i, row := range rows {
		if !strings.Contains(row, needle) {
			continue
		}
		if err := common.RDB.LSet(ctx, key, int64(i), payload).Err(); err != nil {
			common.SysError("notify: room replace failed: " + err.Error())
			return false
		}
		return true
	}
	return false
}

// SetRoomMeta stores the room's title and character name for a reloading guest.
func SetRoomMeta(topic string, payload string) {
	if !Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := roomKey(redisRoomMetaPfx, topic)
	if err := common.RDB.Set(ctx, key, payload, roomTTL).Err(); err != nil {
		common.SysError("notify: room meta failed: " + err.Error())
	}
}

// ReadRoomHistory returns the stored transcript and meta for a room.
func ReadRoomHistory(topic string) (meta string, msgs []string) {
	if !Enabled() {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	meta, _ = common.RDB.Get(ctx, roomKey(redisRoomMetaPfx, topic)).Result()
	msgs, _ = common.RDB.LRange(ctx, roomKey(redisRoomMsgsPfx, topic), 0, -1).Result()
	return meta, msgs
}

// DeleteRoom drops a room's stored state immediately. Closing a room is the
// normal end of its life; waiting out the TTL is not.
func DeleteRoom(topic string) {
	if !Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	common.RDB.Del(ctx,
		roomKey(redisRoomMsgsPfx, topic),
		roomKey(redisRoomMetaPfx, topic),
	)
}

// StartRoomSubscriber fans relayed room frames out to this replica's own
// connections. Safe to call on every replica.
func StartRoomSubscriber() {
	if !Enabled() {
		return
	}
	go subscribeLoop(redisRoomChannel, func(payload string) {
		var frame RoomFrame
		if err := common.UnmarshalJsonStr(payload, &frame); err != nil {
			common.SysError("notify: bad room frame: " + err.Error())
			return
		}
		out, err := common.Marshal(map[string]interface{}{
			"op":    "room",
			"topic": frame.Topic,
			"from":  frame.From,
			"data":  frame.Data,
		})
		if err != nil {
			return
		}
		if frame.ConnID != "" {
			// Addressed: only the replica holding that connection delivers.
			globalHub.sendTo(frame.ConnID, out)
			return
		}
		globalHub.broadcast(out, []string{frame.Topic})
	})
}
