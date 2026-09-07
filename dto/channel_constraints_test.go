package dto

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolvedPinPriorityAndMerge(t *testing.T) {

	t.Run("empty set has no pin", func(t *testing.T) {
		pin, found, overridden := (*ChannelConstraints)(nil).ResolvedPin()
		assert.False(t, found)
		assert.Zero(t, pin.ChannelId)
		assert.Nil(t, overridden)
	})
}
