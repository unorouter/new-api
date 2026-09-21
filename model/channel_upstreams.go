package model

// EnabledBaseUrlsForModelExcept returns the base urls of every other enabled
// channel serving the model, for the guard's last-upstream floor.
func EnabledBaseUrlsForModelExcept(modelName string, channelId int) ([]string, error) {
	var urls []string
	err := DB.Table("abilities").
		Joins("join channels on channels.id = abilities.channel_id").
		Where("abilities.model = ? and abilities.enabled = ? and channels.id <> ?", modelName, true, channelId).
		Distinct("channels.base_url").
		Pluck("channels.base_url", &urls).Error
	return urls, err
}
