package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/go-fuego/fuego"
)

func GetGroups(c fuego.ContextNoBody) (*dto.Response[[]string], error) {
	groupNames := make([]string, 0)
	for groupName := range ratio_setting.GetGroupRatioCopy() {
		groupNames = append(groupNames, groupName)
	}
	return dto.Ok(groupNames)
}

func GetUserGroups(c fuego.ContextNoBody) (*dto.Response[map[string]dto.UserGroupInfo], error) {
	usableGroups := make(map[string]dto.UserGroupInfo)
	userGroup := ""
	userId := dto.UserID(c)
	userGroup, _ = model.GetUserGroup(userId, false)
	userUsableGroups := service.GetUserUsableGroups(userGroup)
	// A failed lookup must not hide every group, so an error leaves them all
	// marked online: the picker degrades to its previous behaviour.
	liveGroups, err := model.GroupsWithEnabledChannel()
	if err != nil {
		common.SysLog("failed to read groups with an enabled channel: " + err.Error())
		liveGroups = nil
	}
	for groupName, _ := range ratio_setting.GetGroupRatioCopy() {
		// UserUsableGroups contains the groups that the user can use
		if desc, ok := userUsableGroups[groupName]; ok {
			usableGroups[groupName] = dto.UserGroupInfo{
				Ratio:  service.GetUserGroupRatio(userGroup, groupName),
				Desc:   desc,
				Online: liveGroups == nil || liveGroups[groupName],
			}
		}
	}
	if _, ok := userUsableGroups["auto"]; ok {
		usableGroups["auto"] = dto.UserGroupInfo{
			Ratio:  "auto",
			Desc:   setting.GetUsableGroupDescription("auto"),
			Online: true,
		}
	}
	return dto.Ok(usableGroups)
}
