package model

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTokenQuotaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB, LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(&Token{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		_ = sqlDB.Close()
	})
	return db
}

func createQuotaTestToken(t *testing.T, db *gorm.DB, token *Token) *Token {
	t.Helper()
	token.UserId = 1
	token.Key = "quota-test-" + fmt.Sprint(time.Now().UnixNano())
	token.Name = "quota-test"
	token.Status = common.TokenStatusEnabled
	require.NoError(t, db.Create(token).Error)
	return token
}

func TestTokenQuotaPeriodsUsesConfiguredTimezone(t *testing.T) {
	t.Setenv("TOKEN_QUOTA_TIMEZONE", "UTC")
	now := time.Date(2026, 9, 9, 15, 4, 5, 0, time.UTC)
	hour, day, month := tokenQuotaPeriods(now)
	assert.Equal(t, time.Date(2026, 9, 9, 15, 0, 0, 0, time.UTC).Unix(), hour)
	assert.Equal(t, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC).Unix(), day)
	assert.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix(), month)
}

func TestTokenQuotaLocationFallsBackToUTC8(t *testing.T) {
	t.Setenv("TOKEN_QUOTA_TIMEZONE", "Not/AZone")
	location := tokenQuotaLocation()
	// 2026-09-09T23:30:00Z is 2026-09-10 07:30 in UTC+8, so the daily window
	// must start at 2026-09-09T16:00:00Z rather than the UTC midnight.
	now := time.Date(2026, 9, 9, 23, 30, 0, 0, time.UTC)
	_, day, _ := tokenQuotaPeriods(now)
	assert.Equal(t, time.Date(2026, 9, 10, 0, 0, 0, 0, location).Unix(), day)
}

func TestResetTokenPeriodQuotaResetsStaleWindows(t *testing.T) {
	t.Setenv("TOKEN_QUOTA_TIMEZONE", "UTC")
	now := time.Now().UTC().Truncate(time.Second)
	prevHour, _, _ := tokenQuotaPeriods(now.Add(-time.Hour))
	_, prevDay, _ := tokenQuotaPeriods(now.Add(-24 * time.Hour))
	_, _, prevMonth := tokenQuotaPeriods(now.AddDate(0, -1, 0))
	token := &Token{
		HourlyInputUsed: 10, HourlyOutputUsed: 11,
		DailyInputUsed: 20, DailyOutputUsed: 21,
		MonthlyInputUsed: 30, MonthlyOutputUsed: 31,
		HourlyPeriodStart:  prevHour - 1,
		DailyPeriodStart:   prevDay - 1,
		MonthlyPeriodStart: prevMonth - 1,
	}
	require.True(t, resetTokenPeriodQuota(token, now))
	currentHour, currentDay, currentMonth := tokenQuotaPeriods(now)
	assert.Equal(t, currentHour, token.HourlyPeriodStart)
	assert.Equal(t, currentDay, token.DailyPeriodStart)
	assert.Equal(t, currentMonth, token.MonthlyPeriodStart)
	assert.Zero(t, token.HourlyInputUsed)
	assert.Zero(t, token.DailyOutputUsed)
	assert.Zero(t, token.MonthlyInputUsed)

	// 同周期内重复调用不得清零已用量。
	token.HourlyInputUsed = 5
	require.False(t, resetTokenPeriodQuota(token, now))
	assert.Equal(t, int64(5), token.HourlyInputUsed)
}

func TestValidateTokenPeriodQuotaLimitsAndParse(t *testing.T) {
	require.NoError(t, ValidateTokenPeriodQuotaLimits(&Token{}))
	require.Error(t, ValidateTokenPeriodQuotaLimits(&Token{HourlyInputLimit: -1}))
	require.Error(t, ValidateTokenPeriodQuotaLimits(&Token{MonthlyOutputLimit: -1}))
	require.Error(t, ValidateTokenPeriodQuotaLimits(nil))

	parsed, err := ParseTokenQuotaInt("123")
	require.NoError(t, err)
	assert.Equal(t, int64(123), parsed)
	_, err = ParseTokenQuotaInt("-1")
	require.Error(t, err)
	_, err = ParseTokenQuotaInt("abc")
	require.Error(t, err)
}

func TestReserveTokenPeriodQuotaLifecycle(t *testing.T) {
	db := setupTokenQuotaTestDB(t)
	token := createQuotaTestToken(t, db, &Token{
		HourlyInputLimit: 100, HourlyOutputLimit: 50,
		DailyInputLimit: 1000, DailyOutputLimit: 500,
		MonthlyInputLimit: 10000, MonthlyOutputLimit: 5000,
	})

	require.NoError(t, ReserveTokenPeriodQuota(token.Id, 40, 20))
	var updated Token
	require.NoError(t, db.First(&updated, token.Id).Error)
	assert.Equal(t, int64(40), updated.HourlyInputUsed)
	assert.Equal(t, int64(20), updated.HourlyOutputUsed)
	assert.Equal(t, int64(40), updated.DailyInputUsed)
	assert.Equal(t, int64(20), updated.DailyOutputUsed)
	assert.Equal(t, int64(40), updated.MonthlyInputUsed)
	assert.Equal(t, int64(20), updated.MonthlyOutputUsed)

	// 40 + 70 > 100:小时输入超限,拒绝且不落账。
	err := ReserveTokenPeriodQuota(token.Id, 70, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenPeriodQuotaExceeded)
	require.NoError(t, db.First(&updated, token.Id).Error)
	assert.Equal(t, int64(40), updated.HourlyInputUsed)

	// 20 + 490 > 500:日输出超限,同样拒绝。
	err = ReserveTokenPeriodQuota(token.Id, 0, 490)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenPeriodQuotaExceeded)

	// 40 + 40 + 20 + 20 恰好达到小时输入 100 与小时输出 50 上限。
	require.NoError(t, ReserveTokenPeriodQuota(token.Id, 60, 30))
	require.NoError(t, db.First(&updated, token.Id).Error)
	assert.Equal(t, int64(100), updated.HourlyInputUsed)
	assert.Equal(t, int64(50), updated.HourlyOutputUsed)
}

func TestSettleTokenPeriodQuotaAdjustsReservation(t *testing.T) {
	db := setupTokenQuotaTestDB(t)
	token := createQuotaTestToken(t, db, &Token{HourlyInputLimit: 100, HourlyOutputLimit: 50})

	require.NoError(t, ReserveTokenPeriodQuota(token.Id, 60, 30))
	// 实际用量低于预占:向下修正。
	require.NoError(t, SettleTokenPeriodQuota(token.Id, 60, 40, 30, 20))
	var updated Token
	require.NoError(t, db.First(&updated, token.Id).Error)
	assert.Equal(t, int64(40), updated.HourlyInputUsed)
	assert.Equal(t, int64(20), updated.HourlyOutputUsed)

	// 结算追加的输出超出剩余额度:拒绝。
	err := SettleTokenPeriodQuota(token.Id, 0, 0, 0, 40)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenPeriodQuotaExceeded)
	require.NoError(t, db.First(&updated, token.Id).Error)
	assert.Equal(t, int64(20), updated.HourlyOutputUsed)
}

func TestSettleTokenPeriodQuotaUnderflowGuard(t *testing.T) {
	db := setupTokenQuotaTestDB(t)
	token := createQuotaTestToken(t, db, &Token{HourlyInputLimit: 100})

	require.NoError(t, ReserveTokenPeriodQuota(token.Id, 10, 0))
	require.NoError(t, SettleTokenPeriodQuota(token.Id, 10, 0, 0, 0))
	// 同一预占重复结算会让计数变负,必须报错并回滚。
	err := SettleTokenPeriodQuota(token.Id, 10, 0, 0, 0)
	require.Error(t, err)
	var updated Token
	require.NoError(t, db.First(&updated, token.Id).Error)
	assert.Equal(t, int64(0), updated.HourlyInputUsed)
	assert.GreaterOrEqual(t, updated.HourlyInputUsed, int64(0))
}

func TestReserveTokenPeriodQuotaEdgeCases(t *testing.T) {
	db := setupTokenQuotaTestDB(t)
	token := createQuotaTestToken(t, db, &Token{})

	// 非法 tokenID 与负数请求直接拒绝,不触发事务。
	require.NoError(t, ReserveTokenPeriodQuota(0, 10, 10))
	require.Error(t, ReserveTokenPeriodQuota(token.Id, -1, 0))
	require.Error(t, ReserveTokenPeriodQuota(token.Id, 0, -1))
	require.Error(t, SettleTokenPeriodQuota(token.Id, -1, 0, 0, 0))
	// 不存在的 token 返回 record not found。
	require.Error(t, ReserveTokenPeriodQuota(999999, 1, 1))
}

func TestReserveTokenPeriodQuotaConcurrentNoOvershoot(t *testing.T) {
	db := setupTokenQuotaTestDB(t)
	token := createQuotaTestToken(t, db, &Token{HourlyInputLimit: 100})

	const workers = 8
	const perReserve = 30
	var wg sync.WaitGroup
	start := make(chan struct{})
	var successCount int64
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := ReserveTokenPeriodQuota(token.Id, perReserve, 0)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
			} else if !errors.Is(err, ErrTokenPeriodQuotaExceeded) {
				// SQLite 单写者模型下可能出现 busy,视为安全拒绝。
				t.Logf("concurrent reserve rejected: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	var updated Token
	require.NoError(t, db.First(&updated, token.Id).Error)
	assert.LessOrEqual(t, updated.HourlyInputUsed, int64(100))
	assert.Equal(t, successCount*perReserve, updated.HourlyInputUsed)
	assert.Greater(t, successCount, int64(0))
}
