package console_setting

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

var (
	urlRegex       = regexp.MustCompile(`^https?://(?:(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?|(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?))(?:\:[0-9]{1,5})?(?:/.*)?$`)
	dangerousChars = []string{"<script", "<iframe", "javascript:", "onload=", "onerror=", "onclick="}
	validColors    = map[string]bool{
		"blue": true, "green": true, "cyan": true, "purple": true, "pink": true,
		"red": true, "orange": true, "amber": true, "yellow": true, "lime": true,
		"light-green": true, "teal": true, "light-blue": true, "indigo": true,
		"violet": true, "grey": true, "slate": true,
	}
	slugRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

func parseJSONArray(jsonStr string, typeName string) ([]map[string]interface{}, error) {
	var list []map[string]interface{}
	if err := common.UnmarshalJsonStr(jsonStr, &list); err != nil {
		return nil, fmt.Errorf("%s format error: %s", typeName, err.Error())
	}
	return list, nil
}

func exceedsMaxCharacters(s string, max int) bool {
	return len(utf16.Encode([]rune(s))) > max
}

func validateURL(urlStr string, index int, itemType string) error {
	if !urlRegex.MatchString(urlStr) {
		return fmt.Errorf("URL format of %s #%d is invalid", itemType, index)
	}
	if _, err := url.Parse(urlStr); err != nil {
		return fmt.Errorf("URL of %s #%d cannot be parsed: %s", itemType, index, err.Error())
	}
	return nil
}

func checkDangerousContent(content string, index int, itemType string) error {
	lower := strings.ToLower(content)
	for _, d := range dangerousChars {
		if strings.Contains(lower, d) {
			return fmt.Errorf("%s #%d contains disallowed content", itemType, index)
		}
	}
	return nil
}

func getJSONList(jsonStr string) []map[string]interface{} {
	if jsonStr == "" {
		return []map[string]interface{}{}
	}
	var list []map[string]interface{}
	_ = common.UnmarshalJsonStr(jsonStr, &list)
	return list
}

func ValidateConsoleSettings(settingsStr string, settingType string) error {
	if settingsStr == "" {
		return nil
	}

	switch settingType {
	case "ApiInfo":
		return validateApiInfo(settingsStr)
	case "Announcements":
		return validateAnnouncements(settingsStr)
	case "FAQ":
		return validateFAQ(settingsStr)
	case "UptimeKumaGroups":
		return validateUptimeKumaGroups(settingsStr)
	default:
		return fmt.Errorf("unknown setting type: %s", settingType)
	}
}

func validateApiInfo(apiInfoStr string) error {
	apiInfoList, err := parseJSONArray(apiInfoStr, "API info")
	if err != nil {
		return err
	}

	if len(apiInfoList) > 50 {
		return fmt.Errorf("the number of API info entries cannot exceed 50")
	}

	for i, apiInfo := range apiInfoList {
		urlStr, ok := apiInfo["url"].(string)
		if !ok || urlStr == "" {
			return fmt.Errorf("API info #%d is missing the URL field", i+1)
		}
		route, ok := apiInfo["route"].(string)
		if !ok || route == "" {
			return fmt.Errorf("API info #%d is missing the route description field", i+1)
		}
		description, ok := apiInfo["description"].(string)
		if !ok || description == "" {
			return fmt.Errorf("API info #%d is missing the description field", i+1)
		}
		color, ok := apiInfo["color"].(string)
		if !ok || color == "" {
			return fmt.Errorf("API info #%d is missing the color field", i+1)
		}

		if err := validateURL(urlStr, i+1, "API info"); err != nil {
			return err
		}

		if exceedsMaxCharacters(urlStr, 500) {
			return fmt.Errorf("the URL of API info #%d cannot exceed 500 characters", i+1)
		}
		if exceedsMaxCharacters(route, 100) {
			return fmt.Errorf("the route description of API info #%d cannot exceed 100 characters", i+1)
		}
		if exceedsMaxCharacters(description, 200) {
			return fmt.Errorf("the description of API info #%d cannot exceed 200 characters", i+1)
		}

		if !validColors[color] {
			return fmt.Errorf("the color value of API info #%d is invalid", i+1)
		}

		if err := checkDangerousContent(description, i+1, "API info"); err != nil {
			return err
		}
		if err := checkDangerousContent(route, i+1, "API info"); err != nil {
			return err
		}
	}
	return nil
}

func GetApiInfo() []dto.ApiInfoEntry {
	cs := GetConsoleSetting()
	if cs.ApiInfo == "" {
		return []dto.ApiInfoEntry{}
	}
	var list []dto.ApiInfoEntry
	if err := common.Unmarshal([]byte(cs.ApiInfo), &list); err != nil {
		return []dto.ApiInfoEntry{}
	}
	return list
}

func validateAnnouncements(announcementsStr string) error {
	list, err := parseJSONArray(announcementsStr, "system announcement")
	if err != nil {
		return err
	}
	if len(list) > 100 {
		return fmt.Errorf("the number of system announcements cannot exceed 100")
	}
	validTypes := map[string]bool{
		"default": true, "ongoing": true, "success": true, "warning": true, "error": true,
	}
	for i, ann := range list {
		content, ok := ann["content"].(string)
		if !ok || content == "" {
			return fmt.Errorf("announcement #%d is missing the content field", i+1)
		}
		publishDateAny, exists := ann["publishDate"]
		if !exists {
			return fmt.Errorf("announcement #%d is missing the publish date field", i+1)
		}
		publishDateStr, ok := publishDateAny.(string)
		if !ok || publishDateStr == "" {
			return fmt.Errorf("the publish date of announcement #%d cannot be empty", i+1)
		}
		if _, err := time.Parse(time.RFC3339, publishDateStr); err != nil {
			return fmt.Errorf("the publish date format of announcement #%d is invalid", i+1)
		}
		if t, exists := ann["type"]; exists {
			if typeStr, ok := t.(string); ok {
				if !validTypes[typeStr] {
					return fmt.Errorf("the type value of announcement #%d is invalid", i+1)
				}
			}
		}
		if exceedsMaxCharacters(content, 500) {
			return fmt.Errorf("the content of announcement #%d cannot exceed 500 characters", i+1)
		}
		if extra, exists := ann["extra"]; exists {
			if extraStr, ok := extra.(string); ok && exceedsMaxCharacters(extraStr, 100) {
				return fmt.Errorf("the description of announcement #%d cannot exceed 100 characters", i+1)
			}
		}
	}
	return nil
}

func validateFAQ(faqStr string) error {
	list, err := parseJSONArray(faqStr, "FAQ info")
	if err != nil {
		return err
	}
	if len(list) > 100 {
		return fmt.Errorf("the number of FAQ entries cannot exceed 100")
	}
	for i, faq := range list {
		question, ok := faq["question"].(string)
		if !ok || question == "" {
			return fmt.Errorf("FAQ #%d is missing the question field", i+1)
		}
		answer, ok := faq["answer"].(string)
		if !ok || answer == "" {
			return fmt.Errorf("FAQ #%d is missing the answer field", i+1)
		}
		if exceedsMaxCharacters(question, 200) {
			return fmt.Errorf("the question of FAQ #%d cannot exceed 200 characters", i+1)
		}
		if exceedsMaxCharacters(answer, 1000) {
			return fmt.Errorf("the answer of FAQ #%d cannot exceed 1000 characters", i+1)
		}
	}
	return nil
}

func getPublishTime(item map[string]interface{}) time.Time {
	if v, ok := item["publishDate"]; ok {
		if s, ok2 := v.(string); ok2 {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func GetAnnouncements() []dto.AnnouncementEntry {
	cs := GetConsoleSetting()
	if cs.Announcements == "" {
		return []dto.AnnouncementEntry{}
	}
	var list []dto.AnnouncementEntry
	if err := common.Unmarshal([]byte(cs.Announcements), &list); err != nil {
		return []dto.AnnouncementEntry{}
	}
	sort.SliceStable(list, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, list[i].PublishDate)
		tj, _ := time.Parse(time.RFC3339, list[j].PublishDate)
		return ti.After(tj)
	})
	return list
}

func GetFAQ() []dto.FAQEntry {
	cs := GetConsoleSetting()
	if cs.FAQ == "" {
		return []dto.FAQEntry{}
	}
	var list []dto.FAQEntry
	if err := common.Unmarshal([]byte(cs.FAQ), &list); err != nil {
		return []dto.FAQEntry{}
	}
	return list
}

func validateUptimeKumaGroups(groupsStr string) error {
	groups, err := parseJSONArray(groupsStr, "Uptime Kuma group config")
	if err != nil {
		return err
	}

	if len(groups) > 20 {
		return fmt.Errorf("the number of Uptime Kuma groups cannot exceed 20")
	}

	nameSet := make(map[string]bool)

	for i, group := range groups {
		categoryName, ok := group["categoryName"].(string)
		if !ok || categoryName == "" {
			return fmt.Errorf("group #%d is missing the category name field", i+1)
		}
		if nameSet[categoryName] {
			return fmt.Errorf("the category name of group #%d duplicates another group", i+1)
		}
		nameSet[categoryName] = true
		urlStr, ok := group["url"].(string)
		if !ok || urlStr == "" {
			return fmt.Errorf("group #%d is missing the URL field", i+1)
		}
		slug, ok := group["slug"].(string)
		if !ok || slug == "" {
			return fmt.Errorf("group #%d is missing the Slug field", i+1)
		}
		description, ok := group["description"].(string)
		if !ok {
			description = ""
		}

		if err := validateURL(urlStr, i+1, "group"); err != nil {
			return err
		}

		if exceedsMaxCharacters(categoryName, 50) {
			return fmt.Errorf("the category name of group #%d cannot exceed 50 characters", i+1)
		}
		if exceedsMaxCharacters(urlStr, 500) {
			return fmt.Errorf("the URL of group #%d cannot exceed 500 characters", i+1)
		}
		if exceedsMaxCharacters(slug, 100) {
			return fmt.Errorf("the Slug of group #%d cannot exceed 100 characters", i+1)
		}
		if exceedsMaxCharacters(description, 200) {
			return fmt.Errorf("the description of group #%d cannot exceed 200 characters", i+1)
		}

		if !slugRegex.MatchString(slug) {
			return fmt.Errorf("the Slug of group #%d can only contain letters, digits, underscores and hyphens", i+1)
		}

		if err := checkDangerousContent(description, i+1, "group"); err != nil {
			return err
		}
		if err := checkDangerousContent(categoryName, i+1, "group"); err != nil {
			return err
		}
	}
	return nil
}

func GetUptimeKumaGroups() []dto.UptimeKumaGroupConfig {
	cs := GetConsoleSetting()
	if cs.UptimeKumaGroups == "" {
		return []dto.UptimeKumaGroupConfig{}
	}
	var list []dto.UptimeKumaGroupConfig
	if err := common.Unmarshal([]byte(cs.UptimeKumaGroups), &list); err != nil {
		return []dto.UptimeKumaGroupConfig{}
	}
	return list
}
