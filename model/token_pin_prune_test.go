package model

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func usePinPruneDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Token{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
	return db
}

func pinOf(t *testing.T, db *gorm.DB, id int) map[string][]string {
	t.Helper()
	var token Token
	require.NoError(t, db.First(&token, id).Error)
	if token.GroupMapping == "" {
		return nil
	}
	var mapping map[string][]string
	require.NoError(t, json.Unmarshal([]byte(token.GroupMapping), &mapping))
	return mapping
}

// A merchant leaving the marketplace deletes its group for good, and a pin that
// still names it 503s every request. A group whose channels are merely
// auto-disabled recovers in minutes and must survive: dropping it would discard
// a deliberate price pin behind the user's back.
func TestPruneDeletedGroupsFromTokenPins(t *testing.T) {
	db := usePinPruneDB(t)

	deletedOnly := &Token{Id: 1, UserId: 7, Key: "k1",
		GroupMapping: `{"glm-5.3-flash":["a6-gone-glm-5.3-flash"]}`}
	partial := &Token{Id: 2, UserId: 7, Key: "k2",
		GroupMapping: `{"glm-5.3":["a6-gone-glm-5.3","a6-live-glm-5.3"]}`}
	disabledButAlive := &Token{Id: 3, UserId: 7, Key: "k3",
		GroupMapping: `{"kimi-k3":["a6-disabled-kimi-k3"]}`}
	untouched := &Token{Id: 4, UserId: 7, Key: "k4",
		GroupMapping: `{"glm-5.3":["a6-live-glm-5.3"]}`}
	for _, token := range []*Token{deletedOnly, partial, disabledButAlive, untouched} {
		require.NoError(t, db.Create(token).Error)
	}

	// a6-disabled-kimi-k3 is present: the group still exists, its channels are
	// just down. a6-gone-* are absent: the sync pruned them.
	live := map[string]bool{
		"a6-live-glm-5.3":     true,
		"a6-disabled-kimi-k3": true,
	}
	changed, err := PruneDeletedGroupsFromTokenPins(live)
	require.NoError(t, err)
	assert.Equal(t, 2, changed)

	// Every group gone -> the model drops out entirely, restoring auto routing
	// rather than leaving an empty list that resolves to nothing.
	assert.Empty(t, pinOf(t, db, 1))
	assert.Equal(t, map[string][]string{"glm-5.3": {"a6-live-glm-5.3"}}, pinOf(t, db, 2))
	assert.Equal(t, map[string][]string{"kimi-k3": {"a6-disabled-kimi-k3"}}, pinOf(t, db, 3))
	assert.Equal(t, map[string][]string{"glm-5.3": {"a6-live-glm-5.3"}}, pinOf(t, db, 4))
}

// A caller that could not read the group list passes an empty map. Treating that
// as "every group is dead" would wipe every pin on the platform.
func TestPruneDeletedGroupsFromTokenPinsIgnoresEmptyLiveSet(t *testing.T) {
	db := usePinPruneDB(t)
	require.NoError(t, db.Create(&Token{Id: 1, UserId: 7, Key: "k1",
		GroupMapping: `{"glm-5.3":["a6-live-glm-5.3"]}`}).Error)

	changed, err := PruneDeletedGroupsFromTokenPins(map[string]bool{})
	require.NoError(t, err)
	assert.Zero(t, changed)
	assert.Equal(t, map[string][]string{"glm-5.3": {"a6-live-glm-5.3"}}, pinOf(t, db, 1))
}
