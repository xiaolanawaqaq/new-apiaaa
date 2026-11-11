package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelStatusTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM abilities").Error)
	require.NoError(t, DB.Exec("DELETE FROM channels").Error)

	memoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = memoryCacheEnabled
	})
}

func TestUpdateChannelStatusPersistsMultiKeyState(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:    "multi-key-status",
		Key:     "key-a\nkey-b",
		Status:  common.ChannelStatusEnabled,
		AutoBan: common.GetPointer(1),
		ChannelInfo: ChannelInfo{
			IsMultiKey:           true,
			MultiKeySize:         2,
			MultiKeyMode:         constant.MultiKeyModePolling,
			MultiKeyPollingIndex: 1,
		},
	}
	require.NoError(t, DB.Create(&channel).Error)

	changed := UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "provider rejected key")
	require.True(t, changed)

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
	assert.Equal(t, "provider rejected key", stored.ChannelInfo.MultiKeyDisabledReason[0])
	assert.NotZero(t, stored.ChannelInfo.MultiKeyDisabledTime[0])
	assert.Equal(t, 1, stored.ChannelInfo.MultiKeyPollingIndex)
}

func TestSaveStatusStateFromSingleKeySnapshotPreservesUnownedColumns(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:        "single-key-status",
		Key:         "original-key",
		Status:      common.ChannelStatusEnabled,
		Models:      "original-model",
		Group:       "default",
		UsedQuota:   100,
		ChannelInfo: ChannelInfo{},
	}
	require.NoError(t, DB.Create(&channel).Error)

	stale, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)

	concurrentChannelInfo := ChannelInfo{
		IsMultiKey:           true,
		MultiKeySize:         2,
		MultiKeyMode:         constant.MultiKeyModePolling,
		MultiKeyPollingIndex: 1,
	}
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"key":          "rotated-key",
		"used_quota":   gorm.Expr("used_quota + ?", 250),
		"models":       "concurrent-model",
		"channel_info": concurrentChannelInfo,
	}).Error)

	stale.Status = common.ChannelStatusManuallyDisabled
	stale.SetOtherInfo(map[string]any{
		"status_reason": "manual operation",
		"status_time":   int64(1234),
	})
	require.NoError(t, stale.saveStatusState())

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.Equal(t, "rotated-key", stored.Key)
	assert.Equal(t, int64(350), stored.UsedQuota)
	assert.Equal(t, "concurrent-model", stored.Models)
	assert.Equal(t, concurrentChannelInfo, stored.ChannelInfo)

	otherInfo := stored.GetOtherInfo()
	assert.Equal(t, "manual operation", otherInfo["status_reason"])
	assert.Equal(t, float64(1234), otherInfo["status_time"])
}

func TestAutoDisableUsesStoredPolicyBeforeCache(t *testing.T) {
	for _, cacheEnabled := range []bool{false, true} {
		for _, multiKey := range []bool{false, true} {
			for _, tc := range []struct {
				name      string
				autoBan   *int
				status    int
				manualKey bool
			}{
				{name: "opted out", autoBan: common.GetPointer(0), status: common.ChannelStatusEnabled},
				{name: "unset auto ban", status: common.ChannelStatusEnabled},
				{name: "manual channel disable", autoBan: common.GetPointer(1), status: common.ChannelStatusManuallyDisabled},
				{name: "manual key disable", autoBan: common.GetPointer(1), status: common.ChannelStatusEnabled, manualKey: true},
				{name: "opt out does not enable old disable", autoBan: common.GetPointer(0), status: common.ChannelStatusAutoDisabled},
			} {
				if tc.manualKey && !multiKey {
					continue
				}
				t.Run(fmt.Sprintf("cache=%t/multi=%t/%s", cacheEnabled, multiKey, tc.name), func(t *testing.T) {
					setupChannelStatusTest(t)
					common.MemoryCacheEnabled = cacheEnabled
					channel := Channel{Name: t.Name(), Key: "key-a", Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1), Models: "test-model", Group: "default"}
					if multiKey {
						channel.Key = "key-a\nkey-b"
						channel.ChannelInfo = ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyPollingIndex: 1}
					}
					require.NoError(t, channel.Insert())
					InitChannelCache()

					freshInfo := channel.ChannelInfo
					if tc.manualKey {
						freshInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusManuallyDisabled}
						freshInfo.MultiKeyDisabledReason = map[int]string{0: "manual key operation"}
						freshInfo.MultiKeyDisabledTime = map[int]int64{0: 1234}
					}
					// Simulate a saved admin change with an older in-flight cache snapshot.
					require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
						"auto_ban":     tc.autoBan,
						"status":       tc.status,
						"channel_info": freshInfo,
					}).Error)
					require.NoError(t, UpdateAbilityStatus(channel.Id, tc.status == common.ChannelStatusEnabled))
					before, err := GetChannelById(channel.Id, true)
					require.NoError(t, err)

					assert.False(t, UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "late provider error"))

					stored, err := GetChannelById(channel.Id, true)
					require.NoError(t, err)
					assert.Equal(t, before.Status, stored.Status)
					assert.Equal(t, before.AutoBan, stored.AutoBan)
					assert.Equal(t, before.OtherInfo, stored.OtherInfo)
					assert.Equal(t, before.ChannelInfo, stored.ChannelInfo)
					var ability Ability
					require.NoError(t, DB.Where("channel_id = ?", channel.Id).First(&ability).Error)
					assert.Equal(t, tc.status == common.ChannelStatusEnabled, ability.Enabled)
					if cacheEnabled {
						cached, err := CacheGetChannel(channel.Id)
						require.NoError(t, err)
						assert.Equal(t, channel.Status, cached.Status)
						assert.Equal(t, channel.ChannelInfo, cached.ChannelInfo)
						assert.Equal(t, channel.OtherInfo, cached.OtherInfo)
						selected, err := GetRandomSatisfiedChannel("default", "test-model", 0, nil)
						require.NoError(t, err)
						require.NotNil(t, selected, "a skipped disable must not remove the cache routing entry")
						assert.Equal(t, channel.Id, selected.Id)
					}
				})
			}
		}
	}
}

func TestAutoDisableLoadFailureLeavesCacheUnchanged(t *testing.T) {
	setupChannelStatusTest(t)
	common.MemoryCacheEnabled = true
	channel := Channel{Name: t.Name(), Key: "key-a", Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(1), Models: "test-model", Group: "default"}
	require.NoError(t, channel.Insert())
	InitChannelCache()
	require.NoError(t, DB.Delete(&channel).Error)

	assert.False(t, UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "late provider error"))
	cached, err := CacheGetChannel(channel.Id)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusEnabled, cached.Status)
	assert.Empty(t, cached.OtherInfo)
}

func TestOptOutStillAllowsManualDisable(t *testing.T) {
	for _, cacheEnabled := range []bool{false, true} {
		for _, usingKey := range []string{"", "key-a"} {
			t.Run(fmt.Sprintf("cache=%t/key=%s", cacheEnabled, usingKey), func(t *testing.T) {
				setupChannelStatusTest(t)
				common.MemoryCacheEnabled = cacheEnabled
				channel := Channel{Name: t.Name(), Key: "key-a\nkey-b", Status: common.ChannelStatusEnabled, AutoBan: common.GetPointer(0), Models: "test-model", Group: "default",
					ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2},
				}
				require.NoError(t, channel.Insert())
				InitChannelCache()
				require.True(t, UpdateChannelStatus(channel.Id, usingKey, common.ChannelStatusManuallyDisabled, "manual operation"))
				stored, err := GetChannelById(channel.Id, true)
				require.NoError(t, err)
				assert.False(t, stored.GetAutoBan())
				if usingKey == "" {
					assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
				} else {
					assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
					assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
				}
			})
		}
	}
}

func TestAutoDisableKeepsManualDisable(t *testing.T) {
	for _, cacheEnabled := range []bool{false, true} {
		for _, tc := range []struct {
			name          string
			multiKey      bool
			initialStatus int
			wantChanged   bool
			wantStatus    int
		}{
			{name: "single key manual disable is kept", initialStatus: common.ChannelStatusManuallyDisabled, wantStatus: common.ChannelStatusManuallyDisabled},
			{name: "multi key manual disable is kept", multiKey: true, initialStatus: common.ChannelStatusManuallyDisabled, wantStatus: common.ChannelStatusManuallyDisabled},
			{name: "single key enabled is auto disabled", initialStatus: common.ChannelStatusEnabled, wantChanged: true, wantStatus: common.ChannelStatusAutoDisabled},
			{name: "multi key enabled is auto disabled", multiKey: true, initialStatus: common.ChannelStatusEnabled, wantChanged: true, wantStatus: common.ChannelStatusAutoDisabled},
		} {
			t.Run(fmt.Sprintf("cache=%t/%s", cacheEnabled, tc.name), func(t *testing.T) {
				setupChannelStatusTest(t)
				common.MemoryCacheEnabled = cacheEnabled

				channel := Channel{Name: t.Name(), Key: "key-a", Status: tc.initialStatus, Models: "test-model", Group: "default", AutoBan: common.GetPointer(1)}
				usingKey := "key-a"
				if tc.multiKey {
					// key-b already failed, so a failure on key-a disables every key.
					channel.Key = "key-a\nkey-b"
					channel.ChannelInfo = ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyStatusList: map[int]int{1: common.ChannelStatusAutoDisabled}}
				}
				channel.SetOtherInfo(map[string]any{"status_reason": "manual operation", "status_time": int64(1234)})
				require.NoError(t, channel.Insert())
				InitChannelCache()

				changed := UpdateChannelStatus(channel.Id, usingKey, common.ChannelStatusAutoDisabled, "provider rejected key")
				assert.Equal(t, tc.wantChanged, changed)

				stored, err := GetChannelById(channel.Id, true)
				require.NoError(t, err)
				assert.Equal(t, tc.wantStatus, stored.Status)
				if !tc.wantChanged {
					assert.Equal(t, "manual operation", stored.GetOtherInfo()["status_reason"])
					assert.Equal(t, float64(1234), stored.GetOtherInfo()["status_time"])
					assert.Equal(t, channel.ChannelInfo.MultiKeyStatusList, stored.ChannelInfo.MultiKeyStatusList)
				}
				var abilities []Ability
				require.NoError(t, DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
				require.NotEmpty(t, abilities)
				for _, ability := range abilities {
					assert.False(t, ability.Enabled)
				}
				if cacheEnabled {
					cached, err := CacheGetChannel(channel.Id)
					require.NoError(t, err)
					assert.Equal(t, tc.wantStatus, cached.Status)
					assert.Equal(t, stored.ChannelInfo.MultiKeyStatusList, cached.ChannelInfo.MultiKeyStatusList)
				}
			})
		}
	}
}
