package relay

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	_ "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/require"
)

// The channel test and legacy task rows address a task adaptor by channel type,
// not by plugin key. Prod's own channel types must resolve to prod's plugins or
// those paths silently get no adaptor.
func TestProdChannelTypesResolveToPlugins(t *testing.T) {
	generation := pluginruntime.DefaultRegistry.Generation()
	for channelType, key := range map[int]string{
		constant.ChannelTypeAIHorde: "aihorde",
		constant.ChannelTypeXai:     "xai",
	} {
		plugin, ok := ResolveTaskPluginForPlatform(generation, constant.TaskPlatform(strconv.Itoa(channelType)))
		require.Truef(t, ok, "channel type %d resolved no plugin", channelType)
		require.Equal(t, key, plugin.Meta.Key)
		require.NotNil(t, GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channelType))))
	}
}
