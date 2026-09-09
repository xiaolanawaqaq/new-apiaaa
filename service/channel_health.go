package service

import (
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// 渠道健康分类。temporary 错误按连续失败计数；balance_exhausted 直接禁用
// 且永不自动探测；其余（认证/参数等）既不计数也不触发禁用。
const (
	ChannelErrorTemporary         = "temporary"
	ChannelErrorBalanceExhausted = "balance_exhausted"
	ChannelErrorUncounted         = "uncounted"
)

// balanceExhaustedKeywords 是上游余额耗尽的典型报错片段。命中任意一条即归类
// balance_exhausted：这类渠道需要人工充值/处理，自动探测没有意义。
var balanceExhaustedKeywords = []string{
	"insufficient balance",
	"insufficient_quota",
	"billing hard limit",
	"exceeded your current quota",
	"credit balance is too low",
	"balance is not enough",
	"account balance",
	"欠费",
	"余额不足",
	"quota exceeded for this billing",
}

// temporaryStatusCodes 是按 HTTP 状态码归类为 temporary 的错误。
var temporaryStatusCodes = map[int]struct{}{
	408: {}, // request timeout
	502: {}, // bad gateway
	503: {}, // service unavailable
	504: {}, // gateway timeout
	522: {}, // connection timed out (cloudflare)
	524: {}, // a timeout occurred
}

// temporaryErrorKeywords 是网络/超时类错误的典型报错片段。
var temporaryErrorKeywords = []string{
	"context deadline exceeded",
	"connection refused",
	"connection reset",
	"connection timed out",
	"unexpected eof",
	"no such host",
	"i/o timeout",
	"tls handshake timeout",
	"proxy error",
	"bad gateway",
	"service unavailable",
}

// ClassifyChannelError 把渠道错误归类为 temporary / balance_exhausted /
// uncounted。分类只看错误文本与状态码，不做任何 I/O。
func ClassifyChannelError(err *types.NewAPIError) string {
	if err == nil {
		return ChannelErrorUncounted
	}
	message := strings.ToLower(err.Error())
	for _, keyword := range balanceExhaustedKeywords {
		if strings.Contains(message, keyword) {
			return ChannelErrorBalanceExhausted
		}
	}
	if _, ok := temporaryStatusCodes[err.StatusCode]; ok {
		return ChannelErrorTemporary
	}
	for _, keyword := range temporaryErrorKeywords {
		if strings.Contains(message, keyword) {
			return ChannelErrorTemporary
		}
	}
	// 未匹配到任何模式的错误（模型不存在、认证失败、参数错误等）不参与
	// 网络失败计数。
	return ChannelErrorUncounted
}

// channelHealth 存储在 channel.OtherInfo JSON 中，重启后状态自动恢复，
// 不需要额外的表结构。所有字段仅在达到阈值时写入。
type channelHealth struct {
	ConsecutiveFailures int    `json:"consecutive_failures"`
	LastFailureTime     int64  `json:"last_failure_time"`
	Classification      string `json:"classification"`
	DisabledReason      string `json:"disabled_reason"`
	NextProbeTime       int64  `json:"next_probe_time"`
	LastProbeTime       int64  `json:"last_probe_time"`
}

const channelHealthInfoKey = "channel_health"

func readChannelHealth(channel *model.Channel) channelHealth {
	var health channelHealth
	info := channel.GetOtherInfo()
	if raw, ok := info[channelHealthInfoKey]; ok {
		if encoded, err := common.Marshal(raw); err == nil {
			_ = common.Unmarshal(encoded, &health)
		}
	}
	return health
}

func writeChannelHealth(channel *model.Channel, health channelHealth) {
	info := channel.GetOtherInfo()
	info[channelHealthInfoKey] = health
	channel.SetOtherInfo(info)
}

// ChannelFailureThreshold 返回触发自动禁用的连续失败次数 N。
func ChannelFailureThreshold() int {
	threshold := common.ChannelFailureThreshold
	if threshold <= 0 {
		threshold = 3
	}
	return threshold
}

// ChannelHealthProbeInterval 返回临时禁用渠道的探测间隔。
func ChannelHealthProbeInterval() time.Duration {
	seconds := common.ChannelHealthProbeIntervalSeconds
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

// RecordChannelFailure 在一次 temporary 类失败后递增计数；达到阈值 N 时把
// 渠道置为自动禁用并记录分类与下次探测时间。balance_exhausted 立即禁用且
// 不安排探测。返回 (分类, 是否触发禁用)。不修改其他渠道字段。
func RecordChannelFailure(channel *model.Channel, err *types.NewAPIError) (string, bool) {
	if channel == nil {
		return ChannelErrorUncounted, false
	}
	classification := ClassifyChannelError(err)
	if classification == ChannelErrorUncounted {
		return classification, false
	}

	health, fresh, skipCount := func() (channelHealth, *model.Channel, bool) {
		pollingLock := model.GetChannelPollingLock(channel.Id)
		pollingLock.Lock()
		defer pollingLock.Unlock()

		// 其他写方可能已更新渠道状态，失败计数必须基于最新数据。
		fresh, loadErr := model.GetChannelById(channel.Id, true)
		if loadErr != nil {
			fresh = channel
		}
		health := readChannelHealth(fresh)
		if fresh.Status != common.ChannelStatusEnabled {
			// 已被禁用的渠道不再累计失败。
			return health, fresh, true
		}
		health.ConsecutiveFailures++
		health.LastFailureTime = common.GetTimestamp()
		health.Classification = classification
		writeChannelHealth(fresh, health)
		persistChannelHealth(fresh)
		return health, fresh, false
	}()
	if skipCount {
		return classification, false
	}

	// DisableChannel -> UpdateChannelStatus 内部获取同一把轮询锁，必须在
	// 锁释放后再调用（见上面临时函数立即执行即释放）。
	if classification == ChannelErrorBalanceExhausted {
		health.DisabledReason = "balance exhausted"
		health.NextProbeTime = 0
		if err := persistHealthOverride(fresh.Id, health); err != nil {
			common.SysLog("channel health: failed to persist health state (channel #" + strconv.Itoa(fresh.Id) + "): " + err.Error())
		}
		DisableChannel(types.ChannelError{
			ChannelId:   fresh.Id,
			ChannelType: fresh.Type,
			ChannelName: fresh.Name,
			IsMultiKey:  fresh.ChannelInfo.IsMultiKey,
			AutoBan:     true,
		}, "balance exhausted: "+common.LocalLogPreview(err.Error()))
		common.SysLog("channel health: channel disabled after balance exhausted (channel #" + strconv.Itoa(fresh.Id) + ")")
		return classification, true
	}

	if health.ConsecutiveFailures >= ChannelFailureThreshold() {
		health.DisabledReason = "consecutive temporary failures"
		health.NextProbeTime = common.GetTimestamp() + int64(ChannelHealthProbeInterval().Seconds())
		if err := persistHealthOverride(fresh.Id, health); err != nil {
			common.SysLog("channel health: failed to persist health state (channel #" + strconv.Itoa(fresh.Id) + "): " + err.Error())
		}
		DisableChannel(types.ChannelError{
			ChannelId:   fresh.Id,
			ChannelType: fresh.Type,
			ChannelName: fresh.Name,
			IsMultiKey:  fresh.ChannelInfo.IsMultiKey,
			AutoBan:     true,
		}, "consecutive failures: "+common.LocalLogPreview(err.Error()))
		common.SysLog("channel health: channel disabled after consecutive failures (channel #" + strconv.Itoa(fresh.Id) + ", count=" + strconv.Itoa(health.ConsecutiveFailures) + ")")
		return classification, true
	}
	return classification, false
}

// persistHealthOverride 在轮询锁外覆写健康字段。DisableChannel 会重写整行
// other_info（含状态原因），这里再把健康状态合并回去，保证健康计数与
// 禁用原因同时可见。
func persistHealthOverride(channelID int, health channelHealth) error {
	pollingLock := model.GetChannelPollingLock(channelID)
	pollingLock.Lock()
	defer pollingLock.Unlock()
	channel, loadErr := model.GetChannelById(channelID, true)
	if loadErr != nil {
		return loadErr
	}
	writeChannelHealth(channel, health)
	return model.DB.Model(&model.Channel{}).Where("id = ?", channelID).
		Update("other_info", channel.OtherInfo).Error
}

// RecordChannelSuccess 在一次成功请求后清零连续失败计数。
func RecordChannelSuccess(channelID int) {
	pollingLock := model.GetChannelPollingLock(channelID)
	pollingLock.Lock()
	defer pollingLock.Unlock()

	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		return
	}
	health := readChannelHealth(channel)
	if health.ConsecutiveFailures == 0 {
		return
	}
	health.ConsecutiveFailures = 0
	health.Classification = ""
	writeChannelHealth(channel, health)
	persistChannelHealth(channel)
}

// IsChannelBalanceExhausted 判断一个自动禁用渠道是否因余额耗尽被禁用。
// 余额耗尽的渠道不参与自动探测，必须人工处理。
func IsChannelBalanceExhausted(channel *model.Channel) bool {
	if channel == nil {
		return false
	}
	return readChannelHealth(channel).Classification == ChannelErrorBalanceExhausted
}

// ChannelDueForProbe 判断渠道是否到达探测时间。balance_exhausted 永不探测。
func ChannelDueForProbe(channel *model.Channel, now time.Time) bool {
	if channel == nil || channel.Status != common.ChannelStatusAutoDisabled {
		return false
	}
	health := readChannelHealth(channel)
	if health.Classification == ChannelErrorBalanceExhausted {
		return false
	}
	if health.NextProbeTime <= 0 {
		return false
	}
	return health.NextProbeTime <= now.Unix()
}

// persistChannelHealth 只落 OtherInfo 一列，避免 Updates 把整行陈旧字段
// 覆盖回去（并发环境下 Name/Models 等可能已被管理员修改）。
func persistChannelHealth(channel *model.Channel) {
	if err := model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).
		Update("other_info", channel.OtherInfo).Error; err != nil {
		common.SysLog("channel health: failed to persist health state (channel #" + strconv.Itoa(channel.Id) + "): " + err.Error())
	}
}

// MarkChannelProbed 更新最近探测时间并顺延下一次探测窗口。
func MarkChannelProbed(channelID int, success bool) {
	pollingLock := model.GetChannelPollingLock(channelID)
	pollingLock.Lock()
	defer pollingLock.Unlock()

	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		return
	}
	health := readChannelHealth(channel)
	health.LastProbeTime = common.GetTimestamp()
	if success {
		// EnableChannel 已负责状态变更与通知，这里只清健康状态。
		health.ConsecutiveFailures = 0
		health.Classification = ""
		health.NextProbeTime = 0
		health.DisabledReason = ""
	} else {
		health.NextProbeTime = common.GetTimestamp() + int64(ChannelHealthProbeInterval().Seconds())
	}
	writeChannelHealth(channel, health)
	persistChannelHealth(channel)
}
