package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/wsmanager"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelHealthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	require.NoError(t, db.AutoMigrate(&model.Ability{}))
	require.NoError(t, db.AutoMigrate(&model.User{}))
	previousDisableFlag := common.AutomaticDisableChannelEnabled
	previousCache, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	previousThreshold, previousProbeInterval := common.ChannelFailureThreshold, common.ChannelHealthProbeIntervalSeconds
	common.AutomaticDisableChannelEnabled = true
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	common.ChannelFailureThreshold, common.ChannelHealthProbeIntervalSeconds = 3, 60
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		common.AutomaticDisableChannelEnabled = previousDisableFlag
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		common.ChannelFailureThreshold, common.ChannelHealthProbeIntervalSeconds = previousThreshold, previousProbeInterval
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	})
	return db
}

func createHealthTestChannel(t *testing.T, db *gorm.DB) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Id:     1,
		Type:   1,
		Key:    "test-key",
		Name:   "health-test-channel",
		Status: common.ChannelStatusEnabled,
		Group:  "default",
		Models: "gpt-test",
	}
	channel.AutoBan = common.GetPointer(1)
	require.NoError(t, db.Create(channel).Error)
	return channel
}

func newStatusError(statusCode int, message string) *types.NewAPIError {
	return types.NewError(fmt.Errorf("%s", message), types.ErrorCode("channel:test"), types.ErrOptionWithStatusCode(statusCode))
}

func TestClassifyChannelError(t *testing.T) {
	// 余额耗尽。
	assert.Equal(t, ChannelErrorBalanceExhausted, ClassifyChannelError(newStatusError(402, "insufficient balance on account")))
	assert.Equal(t, ChannelErrorBalanceExhausted, ClassifyChannelError(newStatusError(429, "You exceeded your current quota, please check your plan and billing details")))
	assert.Equal(t, ChannelErrorBalanceExhausted, ClassifyChannelError(newStatusError(403, "账户余额不足，请充值")))

	// temporary：状态码与网络错误文本。
	assert.Equal(t, ChannelErrorTemporary, ClassifyChannelError(newStatusError(502, "upstream returned error")))
	assert.Equal(t, ChannelErrorTemporary, ClassifyChannelError(newStatusError(504, "gateway timeout")))
	assert.Equal(t, ChannelErrorTemporary, ClassifyChannelError(newStatusError(500, "dial tcp: connection refused")))

	// uncounted：模型不存在、认证失败、参数错误。
	assert.Equal(t, ChannelErrorUncounted, ClassifyChannelError(newStatusError(404, "model not found: gpt-x")))
	assert.Equal(t, ChannelErrorUncounted, ClassifyChannelError(newStatusError(401, "invalid api key")))
	assert.Equal(t, ChannelErrorUncounted, ClassifyChannelError(newStatusError(400, "invalid request payload")))
	assert.Equal(t, ChannelErrorUncounted, ClassifyChannelError(nil))
}

func TestRecordChannelFailureThresholdDisables(t *testing.T) {
	db := setupChannelHealthTestDB(t)
	channel := createHealthTestChannel(t, db)
	temporaryErr := newStatusError(502, "bad gateway")

	// 阈值默认 3：前两次只计数不禁用。
	for i := 1; i < 3; i++ {
		classification, disabled := RecordChannelFailure(channel, temporaryErr)
		assert.Equal(t, ChannelErrorTemporary, classification)
		assert.False(t, disabled)
		var updated model.Channel
		require.NoError(t, db.First(&updated, channel.Id).Error)
		assert.Equal(t, common.ChannelStatusEnabled, updated.Status)
	}

	// 第 3 次达到阈值，自动禁用并安排探测。
	_, disabled := RecordChannelFailure(channel, temporaryErr)
	assert.True(t, disabled)
	var updated model.Channel
	require.NoError(t, db.First(&updated, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, updated.Status)
	health := readChannelHealth(&updated)
	assert.Equal(t, 3, health.ConsecutiveFailures)
	assert.Equal(t, ChannelErrorTemporary, health.Classification)
	assert.Greater(t, health.NextProbeTime, int64(0))

	// 禁用后的失败不再累计。
	_, disabledAgain := RecordChannelFailure(channel, temporaryErr)
	assert.False(t, disabledAgain)
	require.NoError(t, db.First(&updated, channel.Id).Error)
	assert.Equal(t, 3, readChannelHealth(&updated).ConsecutiveFailures)
}

func TestRecordChannelFailureBalanceExhaustedDisablesImmediately(t *testing.T) {
	db := setupChannelHealthTestDB(t)
	channel := createHealthTestChannel(t, db)

	_, disabled := RecordChannelFailure(channel, newStatusError(402, "insufficient balance"))
	assert.True(t, disabled)
	var updated model.Channel
	require.NoError(t, db.First(&updated, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, updated.Status)
	health := readChannelHealth(&updated)
	assert.Equal(t, ChannelErrorBalanceExhausted, health.Classification)
	// 余额耗尽不安排探测。
	assert.Zero(t, health.NextProbeTime)
	// 永不探测。
	assert.False(t, ChannelDueForProbe(&updated, time.Now()))
	assert.True(t, IsChannelBalanceExhausted(&updated))
}

func TestRecordChannelFailureUncountedDoesNothing(t *testing.T) {
	db := setupChannelHealthTestDB(t)
	channel := createHealthTestChannel(t, db)

	_, disabled := RecordChannelFailure(channel, newStatusError(404, "model not found"))
	assert.False(t, disabled)
	var updated model.Channel
	require.NoError(t, db.First(&updated, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, updated.Status)
	assert.Equal(t, 0, readChannelHealth(&updated).ConsecutiveFailures)
}

func TestRecordChannelSuccessResetsCounter(t *testing.T) {
	db := setupChannelHealthTestDB(t)
	channel := createHealthTestChannel(t, db)
	temporaryErr := newStatusError(503, "service unavailable")

	_, _ = RecordChannelFailure(channel, temporaryErr)
	_, _ = RecordChannelFailure(channel, temporaryErr)
	var updated model.Channel
	require.NoError(t, db.First(&updated, channel.Id).Error)
	require.Equal(t, 2, readChannelHealth(&updated).ConsecutiveFailures)

	// 一次成功清零计数，之后仍需 N 次失败才禁用。
	RecordChannelSuccess(channel.Id)
	require.NoError(t, db.First(&updated, channel.Id).Error)
	assert.Equal(t, 0, readChannelHealth(&updated).ConsecutiveFailures)

	_, _ = RecordChannelFailure(channel, temporaryErr)
	_, _ = RecordChannelFailure(channel, temporaryErr)
	_, disabled := RecordChannelFailure(channel, temporaryErr)
	assert.True(t, disabled)
}

func TestChannelHealthStateSurvivesReload(t *testing.T) {
	db := setupChannelHealthTestDB(t)
	channel := createHealthTestChannel(t, db)

	_, _ = RecordChannelFailure(channel, newStatusError(502, "bad gateway"))
	_, _ = RecordChannelFailure(channel, newStatusError(504, "gateway timeout"))
	_, disabled := RecordChannelFailure(channel, newStatusError(502, "bad gateway"))
	require.True(t, disabled)

	// 重新从数据库加载（模拟进程重启），计数与分类必须恢复。
	reloaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	health := readChannelHealth(reloaded)
	assert.Equal(t, 3, health.ConsecutiveFailures)
	assert.Equal(t, ChannelErrorTemporary, health.Classification)
	assert.True(t, ChannelDueForProbe(reloaded, time.Now().Add(2*time.Minute)))
}

func TestMarkChannelProbedPostponesAndClears(t *testing.T) {
	db := setupChannelHealthTestDB(t)
	channel := createHealthTestChannel(t, db)
	_, _ = RecordChannelFailure(channel, newStatusError(502, "bad gateway"))
	_, _ = RecordChannelFailure(channel, newStatusError(504, "gateway timeout"))
	_, disabled := RecordChannelFailure(channel, newStatusError(502, "bad gateway"))
	require.True(t, disabled)

	// 探测失败：顺延探测窗口。
	MarkChannelProbed(channel.Id, false)
	reloaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	health := readChannelHealth(reloaded)
	assert.NotZero(t, health.LastProbeTime)
	assert.Greater(t, health.NextProbeTime, time.Now().Unix())
	// 渠道仍是禁用。
	assert.Equal(t, common.ChannelStatusAutoDisabled, reloaded.Status)

	// 探测成功：清零健康状态。
	MarkChannelProbed(channel.Id, true)
	reloaded, err = model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	health = readChannelHealth(reloaded)
	assert.Equal(t, 0, health.ConsecutiveFailures)
	assert.Equal(t, "", health.Classification)
	assert.Zero(t, health.NextProbeTime)
}

func TestRecordChannelFailureSkipsDisabledPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		globalEnabled bool
		autoBan       *int
		status        int
	}{
		{name: "global off", autoBan: common.GetPointer(1), status: common.ChannelStatusEnabled},
		{name: "latest channel opted out", globalEnabled: true, autoBan: common.GetPointer(0), status: common.ChannelStatusEnabled},
		{name: "unset auto ban", globalEnabled: true, status: common.ChannelStatusEnabled},
		{name: "manual disable is preserved", globalEnabled: true, autoBan: common.GetPointer(1), status: common.ChannelStatusManuallyDisabled},
		{name: "opt out does not enable old disabled channel", globalEnabled: true, autoBan: common.GetPointer(0), status: common.ChannelStatusAutoDisabled},
	} {
		for _, failure := range []struct {
			classification string
			err            *types.NewAPIError
		}{
			{ChannelErrorTemporary, newStatusError(502, "bad gateway")},
			{ChannelErrorBalanceExhausted, newStatusError(402, "insufficient balance")},
		} {
			t.Run(tc.name+"/"+failure.classification, func(t *testing.T) {
				db := setupChannelHealthTestDB(t)
				channel := createHealthTestChannel(t, db)
				writeChannelHealth(channel, channelHealth{ConsecutiveFailures: 2, LastFailureTime: 1234, Classification: ChannelErrorTemporary})
				require.NoError(t, db.Model(channel).Update("other_info", channel.OtherInfo).Error)
				// Keep the in-flight snapshot enabled and opted in while the saved policy changes.
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
					"auto_ban": tc.autoBan,
					"status":   tc.status,
				}).Error)
				common.AutomaticDisableChannelEnabled = tc.globalEnabled

				for range 3 {
					classification, disabled := RecordChannelFailure(channel, failure.err)
					assert.Equal(t, failure.classification, classification)
					assert.False(t, disabled)
				}
				stored, err := model.GetChannelById(channel.Id, true)
				require.NoError(t, err)
				assert.Equal(t, tc.status, stored.Status)
				assert.Equal(t, channel.OtherInfo, stored.OtherInfo, "no count, disable reason, or probe may be written")
				assert.Equal(t, tc.autoBan, stored.AutoBan)
			})
		}
	}
}

func TestRecordChannelFailureFailsClosedOnLoadError(t *testing.T) {
	db := setupChannelHealthTestDB(t)
	channel := createHealthTestChannel(t, db)
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("channel_health_load_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "channels" {
			tx.AddError(errors.New("channel load failed"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove("channel_health_load_failure")) })

	classification, disabled := RecordChannelFailure(channel, newStatusError(402, "insufficient balance"))
	assert.Equal(t, ChannelErrorBalanceExhausted, classification)
	assert.False(t, disabled)
	assert.Empty(t, channel.OtherInfo, "must not fall back to and mutate the stale snapshot")
	require.NoError(t, db.Callback().Query().Remove("channel_health_load_failure"))
	stored, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Empty(t, stored.OtherInfo)
}

func TestRecordChannelFailureDoesNotScheduleProbeWhenDisableFails(t *testing.T) {
	db := setupChannelHealthTestDB(t)
	channel := createHealthTestChannel(t, db)
	common.ChannelFailureThreshold = 1
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("channel_health_status_failure", func(tx *gorm.DB) {
		if updates, ok := tx.Statement.Dest.(map[string]any); ok && tx.Statement.Table == "channels" {
			if _, changingStatus := updates["status"]; changingStatus {
				tx.AddError(errors.New("channel status update failed"))
			}
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove("channel_health_status_failure")) })

	classification, disabled := RecordChannelFailure(channel, newStatusError(502, "bad gateway"))
	assert.Equal(t, ChannelErrorTemporary, classification)
	assert.False(t, disabled)
	stored, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	health := readChannelHealth(stored)
	assert.Equal(t, 1, health.ConsecutiveFailures)
	assert.Empty(t, health.DisabledReason)
	assert.Zero(t, health.NextProbeTime)
}

func TestDisableChannelSkippedHasNoSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name          string
		globalEnabled bool
		snapshotBan   bool
		savedBan      int
	}{
		{name: "global off", snapshotBan: true, savedBan: 1},
		{name: "snapshot opted out", globalEnabled: true, savedBan: 1},
		{name: "saved channel opted out", globalEnabled: true, snapshotBan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupChannelHealthTestDB(t)
			channel := createHealthTestChannel(t, db)
			require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("auto_ban", tc.savedBan).Error)
			common.AutomaticDisableChannelEnabled = tc.globalEnabled
			closed := false
			t.Cleanup(wsmanager.Register(channel.Id, wsmanager.KindResponses, func(string) { closed = true }))
			root := &model.User{Username: "channel-watch-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
			require.NoError(t, db.Create(root).Error)
			notifyKey := fmt.Sprintf("%d:%s:%s", root.Id, formatNotifyType(channel.Id, common.ChannelStatusAutoDisabled), time.Now().Format("2006010215"))
			notifyLimitStore.Delete(notifyKey)
			t.Cleanup(func() { notifyLimitStore.Delete(notifyKey) })

			DisableChannel(types.ChannelError{ChannelId: channel.Id, ChannelName: channel.Name, AutoBan: tc.snapshotBan}, "Insufficient balance")
			stored, err := model.GetChannelById(channel.Id, true)
			require.NoError(t, err)
			assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
			assert.Empty(t, stored.OtherInfo)
			assert.False(t, closed)
			_, notified := notifyLimitStore.Load(notifyKey)
			assert.False(t, notified)
		})
	}
}

func TestChannelFailureThresholdConfig(t *testing.T) {
	previous := common.ChannelFailureThreshold
	t.Cleanup(func() { common.ChannelFailureThreshold = previous })

	common.ChannelFailureThreshold = 0
	assert.Equal(t, 3, ChannelFailureThreshold())
	common.ChannelFailureThreshold = 5
	assert.Equal(t, 5, ChannelFailureThreshold())
}
