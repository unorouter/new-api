package model

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// RenameGroupInTokens moves every token reference to group from over to group to:
// the base group, each per-model pin and the stored auto list. The sync renames a
// lane's group when what the lane serves changes (a "-nothink" suffix), and a pin
// left on the old name would silently fall back to auto routing.
func RenameGroupInTokens(from, to string) (int, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" || from == to {
		return 0, nil
	}
	quoted := "%\"" + from + "\"%"
	var tokens []Token
	err := DB.Model(&Token{}).
		Where(commonGroupCol+" = ? OR group_mapping LIKE ? OR auto_groups LIKE ?", from, quoted, quoted).
		Find(&tokens).Error
	if err != nil {
		return 0, err
	}
	changed := 0
	for i := range tokens {
		token := &tokens[i]
		updates := map[string]any{}
		if token.Group == from {
			updates["group"] = to
		}
		if mapping, ok := renameInPins(token.GroupMapping, from, to); ok {
			updates["group_mapping"] = mapping
		}
		if auto, ok := renameInList(token.AutoGroups, from, to); ok {
			updates["auto_groups"] = auto
		}
		if len(updates) == 0 {
			continue
		}
		if err := invalidateTokenCacheForMutation(token.Key); err != nil {
			return changed, err
		}
		if err := DB.Model(&Token{}).Where("id = ?", token.Id).Updates(updates).Error; err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func renameInPins(raw, from, to string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return "", false
	}
	var pins map[string]tokenPinEntryShape
	if err := common.UnmarshalJsonStr(raw, &pins); err != nil {
		return "", false
	}
	moved := false
	for name, entry := range pins {
		groups, ok := renameGroup(entry.Groups, from, to)
		if !ok {
			continue
		}
		entry.Groups = groups
		pins[name] = entry
		moved = true
	}
	if !moved {
		return "", false
	}
	encoded, err := common.Marshal(pins)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func renameInList(raw, from, to string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	var groups []string
	if err := common.UnmarshalJsonStr(raw, &groups); err != nil {
		return "", false
	}
	renamed, ok := renameGroup(groups, from, to)
	if !ok {
		return "", false
	}
	encoded, err := common.Marshal(renamed)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// renameGroup keeps the pin's order; a list already holding the new name just drops the old one.
func renameGroup(groups []string, from, to string) ([]string, bool) {
	at := slices.Index(groups, from)
	if at < 0 {
		return groups, false
	}
	out := slices.Clone(groups)
	if slices.Contains(out, to) {
		return slices.Delete(out, at, at+1), true
	}
	out[at] = to
	return out, true
}
