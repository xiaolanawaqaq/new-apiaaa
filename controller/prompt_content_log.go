package controller

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func requirePromptSecurityAdmin(c *gin.Context) bool {
	if c.GetInt("role") < common.RoleAdminUser {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "管理员权限不足",
		})
		return false
	}
	return true
}

// GetPromptSecurityOptions exposes the security options using the same Option
// shape as the existing administrator settings API.
func GetPromptSecurityOptions(c *gin.Context) {
	if !requirePromptSecurityAdmin(c) {
		return
	}
	common.OptionMapRWMutex.RLock()
	blacklist := common.OptionMap[service.PromptBlacklistOptionKey]
	enabled := common.OptionMap[service.SecureContentLogEnabledOptionKey]
	retention := common.OptionMap[service.SecureContentLogRetentionOptionKey]
	maxBytes := common.OptionMap[service.SecureContentLogMaxBytesOptionKey]
	common.OptionMapRWMutex.RUnlock()
	if enabled == "" {
		enabled = "false"
	}
	if retention == "" {
		retention = "24"
	}
	if maxBytes == "" {
		maxBytes = strconv.Itoa(model.MaxPromptContentLogBytes)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": []*model.Option{
			{Key: service.PromptBlacklistOptionKey, Value: blacklist},
			{Key: service.SecureContentLogEnabledOptionKey, Value: enabled},
			{Key: service.SecureContentLogRetentionOptionKey, Value: retention},
			{Key: service.SecureContentLogMaxBytesOptionKey, Value: maxBytes},
		},
	})
}

type PromptSecurityOptionUpdateRequest struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

func promptSecurityOptionString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// UpdatePromptSecurityOption persists only the security options owned by this
// module. Generic Option writes remain available through the existing root API.
func UpdatePromptSecurityOption(c *gin.Context) {
	if !requirePromptSecurityAdmin(c) {
		return
	}
	var request PromptSecurityOptionUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的参数"})
		return
	}
	value := promptSecurityOptionString(request.Value)
	switch request.Key {
	case service.PromptBlacklistOptionKey:
		if _, err := service.ParsePromptBlacklist(value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
		if err := service.SetPromptBlacklist(value); err != nil {
			common.ApiError(c, err)
			return
		}
	case service.SecureContentLogEnabledOptionKey:
		if !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "SecureContentLogEnabled 必须是布尔值"})
			return
		}
		if err := model.UpdateOption(request.Key, strings.ToLower(value)); err != nil {
			common.ApiError(c, err)
			return
		}
	case service.SecureContentLogRetentionOptionKey:
		hours, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || hours <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "SecureContentLogRetentionHours 必须是正整数"})
			return
		}
		if err := model.UpdateOption(request.Key, strconv.Itoa(hours)); err != nil {
			common.ApiError(c, err)
			return
		}
	case service.SecureContentLogMaxBytesOptionKey:
		maxBytes, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || maxBytes <= 0 || maxBytes > model.MaxSecureContentLogBytesLimit {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "SecureContentLogMaxBytes 必须是 1 到 65536 之间的整数"})
			return
		}
		if err := model.UpdateOption(request.Key, strconv.Itoa(maxBytes)); err != nil {
			common.ApiError(c, err)
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "不支持的安全配置项"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func GetPromptContentLogs(c *gin.Context) {
	if !requirePromptSecurityAdmin(c) {
		return
	}
	page := common.GetPageQuery(c)
	userID, _ := strconv.Atoi(c.Query("user_id"))
	tokenID, _ := strconv.Atoi(c.Query("token_id"))
	channelID, _ := strconv.Atoi(c.Query("channel_id"))
	startAt, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endAt, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	entries, total, err := model.GetPromptContentLogs(model.PromptContentLogFilter{
		UserID:    userID,
		TokenID:   tokenID,
		ChannelID: channelID,
		RequestID: c.Query("request_id"),
		ModelName: c.Query("model_name"),
		Direction: c.Query("direction"),
		StartAt:   startAt,
		EndAt:     endAt,
	}, page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	// 查询时顺带清理过期记录（小批量、尽力而为），配合每小时的定时清理任务。
	if _, err := model.DeleteExpiredPromptContentLogs(context.Background(), 0, 100); err != nil {
		logger.LogWarn(c, "prompt content log opportunistic cleanup failed: "+err.Error())
	}
	page.SetTotal(int(total))
	page.SetItems(entries)
	common.ApiSuccess(c, page)
}

func CleanupPromptContentLogs(c *gin.Context) {
	if !requirePromptSecurityAdmin(c) {
		return
	}
	removed, err := model.DeleteExpiredPromptContentLogs(context.Background(), 0, 5000)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"deleted": removed}})
}
