package model

import (
	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm/clause"
)

// ModelStatusComponent is the page-level row shown on the public status page,
// one per model. Auto-created by the snapshot worker on first probe; admin
// can later edit Description / GroupId / SortOrder / Public.
type ModelStatusComponent struct {
	Id          int    `json:"id" gorm:"primaryKey;autoIncrement"`
	ModelName   string `json:"name" gorm:"type:varchar(255);uniqueIndex"`
	Description string `json:"description" gorm:"type:text"`
	GroupId     *int   `json:"group_id,omitempty" gorm:"index"`
	SortOrder   int    `json:"sort_order" gorm:"default:0"`
	Public      bool   `json:"public" gorm:"default:true"`
	CreatedAt   int64  `json:"created_at" gorm:"bigint"`
	// LastUpAt is bumped by the snapshot worker whenever the model has at
	// least one enabled channel. Public reads hide components that have been
	// continuously down longer than StatusHideAfterSeconds.
	LastUpAt int64 `json:"last_up_at" gorm:"bigint;default:0"`
}

// StatusHideAfterSeconds: a model or channel with zero enabled capacity for
// this long is a dead catalog entry, not an outage - the public status page
// hides the model and the snapshot worker drops the channel from structural
// totals. Both reappear the minute a channel comes back up.
const StatusHideAfterSeconds = 7 * 24 * 60 * 60

func (ModelStatusComponent) TableName() string {
	return "model_status_components"
}

// UpsertModelStatusComponents inserts a component row for any model not yet
// known. Existing rows are left untouched (admin edits to description etc.
// are preserved).
func UpsertModelStatusComponents(modelNames []string) error {
	if len(modelNames) == 0 {
		return nil
	}
	now := common.GetTimestamp()
	rows := make([]*ModelStatusComponent, 0, len(modelNames))
	for _, name := range modelNames {
		rows = append(rows, &ModelStatusComponent{
			ModelName: name,
			Public:    true,
			CreatedAt: now,
		})
	}
	return DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func GetAllPublicModelStatusComponents() ([]*ModelStatusComponent, error) {
	var rows []*ModelStatusComponent
	cutoff := common.GetTimestamp() - StatusHideAfterSeconds
	// created_at grace keeps a brand-new model visible while it has never
	// been up yet (fresh outage is signal; week-dead is noise).
	err := DB.Where("public = ?", true).
		Where("last_up_at >= ? OR created_at >= ?", cutoff, cutoff).
		Order("sort_order ASC, model_name ASC").
		Find(&rows).Error
	return rows, err
}

// BumpModelStatusComponentsLastUp stamps last_up_at for every model that
// currently has at least one enabled channel. Called once per snapshot
// minute by the worker.
func BumpModelStatusComponentsLastUp(upModels []string, ts int64) error {
	if len(upModels) == 0 {
		return nil
	}
	return DB.Model(&ModelStatusComponent{}).
		Where("model_name IN ?", upModels).
		Update("last_up_at", ts).Error
}

// GetComponentByModel resolves a component row for a model name. Used by the
// incident state machine in the worker.
func GetComponentByModel(modelName string) (*ModelStatusComponent, error) {
	var row ModelStatusComponent
	err := DB.Where("model_name = ?", modelName).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// DeleteModelStatusComponentsNotIn removes component rows whose model_name is
// not in the given active set. Caller MUST guard against an empty slice; an
// empty active set is treated as a no-op to avoid wiping the table when the
// snapshot worker temporarily fails to enumerate channels.
func DeleteModelStatusComponentsNotIn(activeModels []string) error {
	if len(activeModels) == 0 {
		return nil
	}
	return DB.Where("model_name NOT IN ?", activeModels).
		Delete(&ModelStatusComponent{}).Error
}
