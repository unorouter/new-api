package model

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// migrateTokenGroupMappingShape rewrites a token's per-model pins from the
// original array form ({"model":["group",...]}) to the entry form
// ({"model":{"groups":["group",...]}}), which also carries the optional price
// band and the per-model auto flag.
//
// The parser accepts only one shape, so an unmigrated row would parse as nil
// and silently unpin every model on that key. Rows are rewritten one at a time
// rather than in a single statement because the payloads are JSON text that no
// supported database can transform portably.
//
// Idempotent: a row already in the entry form is skipped, and a row that parses
// as neither shape is left untouched and logged rather than destroyed.
func migrateTokenGroupMappingShape(db *gorm.DB) error {
	var tokens []Token
	err := db.Model(&Token{}).
		Select("id", "group_mapping").
		Where("group_mapping IS NOT NULL AND group_mapping <> ? AND group_mapping <> ?", "", "{}").
		Find(&tokens).Error
	if err != nil {
		return err
	}
	migrated := 0
	for _, token := range tokens {
		raw := strings.TrimSpace(token.GroupMapping)
		if raw == "" || raw == "{}" {
			continue
		}
		// Already migrated: the entry form parses as objects, the legacy form does not.
		var current map[string]tokenPinEntryShape
		if err := common.UnmarshalJsonStr(raw, &current); err == nil {
			continue
		}
		var legacy map[string][]string
		if err := common.UnmarshalJsonStr(raw, &legacy); err != nil {
			common.SysError("token group_mapping migration skipped unrecognized payload on token " +
				strconv.Itoa(token.Id) + ": " + err.Error())
			continue
		}
		converted := make(map[string]tokenPinEntryShape, len(legacy))
		for modelName, groups := range legacy {
			if groups == nil {
				groups = []string{}
			}
			converted[modelName] = tokenPinEntryShape{Groups: groups}
		}
		encoded, err := common.Marshal(converted)
		if err != nil {
			common.SysError("token group_mapping migration failed to encode token " +
				strconv.Itoa(token.Id) + ": " + err.Error())
			continue
		}
		if err := db.Model(&Token{}).Where("id = ?", token.Id).
			Update("group_mapping", string(encoded)).Error; err != nil {
			return err
		}
		migrated++
	}
	if migrated > 0 {
		common.SysLog("migrated " + strconv.Itoa(migrated) + " token group_mapping rows to the entry shape")
	}
	return nil
}

// tokenPinEntryShape mirrors service.TokenPinEntry. It is duplicated here
// because model must not import service, and the migration only needs the
// wire shape rather than the resolution behavior.
type tokenPinEntryShape struct {
	Groups []string `json:"groups"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
	Auto   bool     `json:"auto,omitempty"`
}
