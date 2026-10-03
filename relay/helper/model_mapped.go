package helper

import (
	"errors"
	"fmt"

	rootcommon "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/gin-gonic/gin"
)

var effortLadder = []reasoning.Effort{
	reasoning.EffortNone,
	reasoning.EffortMinimal,
	reasoning.EffortLow,
	reasoning.EffortMedium,
	reasoning.EffortHigh,
	reasoning.EffortXHigh,
	reasoning.EffortMax,
}

func ModelMappedHelper(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) error {
	if info.ChannelMeta == nil {
		info.ChannelMeta = &relaycommon.ChannelMeta{}
	}
	info.EffortVariantModel = ""

	// map model name
	modelMapping := c.GetString("model_mapping")
	if modelMapping != "" && modelMapping != "{}" {
		modelMap := make(map[string]string)
		err := rootcommon.Unmarshal([]byte(modelMapping), &modelMap)
		if err != nil {
			return fmt.Errorf("unmarshal_model_mapping_failed")
		}

		variant, hasVariants := effortVariant(info, modelMap)
		if variant != "" {
			info.EffortVariantModel = variant
			info.IsModelMapped = true
		} else {
			// 支持链式模型重定向，最终使用链尾的模型
			currentModel := info.OriginModelName
			visitedModels := map[string]bool{
				currentModel: true,
			}
			for {
				mappedModel, exists := modelMap[currentModel]
				baseModel := hostreasoning.BaseModelName(currentModel)
				if (!exists || mappedModel == "") && baseModel != currentModel {
					mappedModel, exists = modelMap[baseModel]
				}
				if exists && mappedModel != "" {
					// 模型重定向循环检测，避免无限循环
					if visitedModels[mappedModel] {
						if mappedModel == currentModel {
							if currentModel == info.OriginModelName {
								info.IsModelMapped = false
								return nil
							}

							info.IsModelMapped = true
							break
						}
						return errors.New("model_mapping_contains_cycle")
					}
					visitedModels[mappedModel] = true
					currentModel = mappedModel
					info.IsModelMapped = true
				} else {
					break
				}
			}
			if info.IsModelMapped {
				info.UpstreamModelName = currentModel
				// The plain key names the default of an effort set, a real ID too.
				if hasVariants {
					info.EffortVariantModel = currentModel
				}
			}
		}
		if info.EffortVariantModel != "" {
			info.UpstreamModelName = info.EffortVariantModel
		}
	}

	if request != nil {
		request.SetModelName(info.UpstreamModelName)
	}
	return nil
}

// effortVariant resolves `<model>@effort:<effort>` keys for upstreams that sell one model as an ID
// per effort; an effort without a key falls through to the plain mapping, effort field forwarded.
func effortVariant(info *relaycommon.RelayInfo, modelMap map[string]string) (string, bool) {
	parsed, err := parseRequestModelName(info.OriginModelName, info.ConvOptions())
	if err != nil {
		return "", false
	}
	hasVariants := false
	for _, effort := range effortLadder {
		if modelMap[parsed.base+"@effort:"+string(effort)] != "" {
			hasVariants = true
			break
		}
	}
	if !hasVariants {
		return "", false
	}
	raw := info.ReasoningEffort
	if parsed.hasThinking {
		raw = string(reasoning.EffectiveEffort(parsed.intent))
	}
	effort, err := reasoning.ParseEffort(raw)
	if err != nil || effort == "" {
		return "", true
	}
	return modelMap[parsed.base+"@effort:"+string(effort)], true
}
