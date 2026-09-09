package model

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// TokenPeriodQuota stores independent input/output token budgets and usage for
// hourly, daily, and monthly periods. A zero limit means unlimited.
type TokenPeriodQuota struct {
	HourlyInputLimit   int64 `json:"hourly_input_limit" gorm:"default:0"`
	HourlyOutputLimit  int64 `json:"hourly_output_limit" gorm:"default:0"`
	DailyInputLimit    int64 `json:"daily_input_limit" gorm:"default:0"`
	DailyOutputLimit   int64 `json:"daily_output_limit" gorm:"default:0"`
	MonthlyInputLimit  int64 `json:"monthly_input_limit" gorm:"default:0"`
	MonthlyOutputLimit int64 `json:"monthly_output_limit" gorm:"default:0"`

	HourlyInputUsed   int64 `json:"hourly_input_used" gorm:"default:0"`
	HourlyOutputUsed  int64 `json:"hourly_output_used" gorm:"default:0"`
	DailyInputUsed    int64 `json:"daily_input_used" gorm:"default:0"`
	DailyOutputUsed   int64 `json:"daily_output_used" gorm:"default:0"`
	MonthlyInputUsed  int64 `json:"monthly_input_used" gorm:"default:0"`
	MonthlyOutputUsed int64 `json:"monthly_output_used" gorm:"default:0"`

	HourlyPeriodStart  int64 `json:"hourly_period_start" gorm:"default:0"`
	DailyPeriodStart   int64 `json:"daily_period_start" gorm:"default:0"`
	MonthlyPeriodStart int64 `json:"monthly_period_start" gorm:"default:0"`
}

var ErrTokenPeriodQuotaExceeded = errors.New("token period quota exceeded")

func tokenQuotaLocation() *time.Location {
	name := strings.TrimSpace(os.Getenv("TOKEN_QUOTA_TIMEZONE"))
	if name == "" {
		name = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("UTC+8", 8*60*60)
	}
	return location
}

func tokenQuotaPeriods(now time.Time) (hour, day, month int64) {
	local := now.In(tokenQuotaLocation())
	hour = time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, local.Location()).Unix()
	day = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location()).Unix()
	month = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, local.Location()).Unix()
	return
}

func resetTokenPeriodQuota(q *Token, now time.Time) bool {
	hour, day, month := tokenQuotaPeriods(now)
	changed := false
	if q.HourlyPeriodStart != hour {
		q.HourlyPeriodStart = hour
		q.HourlyInputUsed, q.HourlyOutputUsed = 0, 0
		changed = true
	}
	if q.DailyPeriodStart != day {
		q.DailyPeriodStart = day
		q.DailyInputUsed, q.DailyOutputUsed = 0, 0
		changed = true
	}
	if q.MonthlyPeriodStart != month {
		q.MonthlyPeriodStart = month
		q.MonthlyInputUsed, q.MonthlyOutputUsed = 0, 0
		changed = true
	}
	return changed
}

func tokenQuotaCheck(q *Token, input, output int64) error {
	if input < 0 || output < 0 {
		return fmt.Errorf("token usage cannot be negative")
	}
	checks := []struct {
		name      string
		limit     int64
		used      int64
		requested int64
	}{
		{"hourly_input", q.HourlyInputLimit, q.HourlyInputUsed, input},
		{"hourly_output", q.HourlyOutputLimit, q.HourlyOutputUsed, output},
		{"daily_input", q.DailyInputLimit, q.DailyInputUsed, input},
		{"daily_output", q.DailyOutputLimit, q.DailyOutputUsed, output},
		{"monthly_input", q.MonthlyInputLimit, q.MonthlyInputUsed, input},
		{"monthly_output", q.MonthlyOutputLimit, q.MonthlyOutputUsed, output},
	}
	for _, check := range checks {
		if check.limit > 0 && check.used+check.requested > check.limit {
			return fmt.Errorf("%w: %s limit=%d used=%d requested=%d", ErrTokenPeriodQuotaExceeded, check.name, check.limit, check.used, check.requested)
		}
	}
	return nil
}

func applyTokenQuotaDelta(q *Token, input, output int64) {
	q.HourlyInputUsed += input
	q.HourlyOutputUsed += output
	q.DailyInputUsed += input
	q.DailyOutputUsed += output
	q.MonthlyInputUsed += input
	q.MonthlyOutputUsed += output
}

func validateTokenQuotaLimits(q *Token) error {
	limits := []int64{q.HourlyInputLimit, q.HourlyOutputLimit, q.DailyInputLimit, q.DailyOutputLimit, q.MonthlyInputLimit, q.MonthlyOutputLimit}
	for _, limit := range limits {
		if limit < 0 {
			return errors.New("token period quota limits cannot be negative")
		}
	}
	return nil
}

func ValidateTokenPeriodQuotaLimits(q *Token) error {
	if q == nil {
		return errors.New("token is nil")
	}
	return validateTokenQuotaLimits(q)
}

// ReserveTokenPeriodQuota atomically resets periods, checks all three windows,
// and adds the requested reservation while holding a row lock.
func ReserveTokenPeriodQuota(tokenID int, input, output int64) error {
	if tokenID <= 0 {
		return nil
	}
	if input < 0 || output < 0 {
		return errors.New("token period quota reservation cannot be negative")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var token Token
		if err := lockForUpdate(tx).First(&token, tokenID).Error; err != nil {
			return err
		}
		if err := validateTokenQuotaLimits(&token); err != nil {
			return err
		}
		resetTokenPeriodQuota(&token, time.Now())
		if err := tokenQuotaCheck(&token, input, output); err != nil {
			return err
		}
		applyTokenQuotaDelta(&token, input, output)
		return tx.Model(&token).Select("hourly_input_used", "hourly_output_used", "daily_input_used", "daily_output_used", "monthly_input_used", "monthly_output_used", "hourly_period_start", "daily_period_start", "monthly_period_start").Updates(&token).Error
	})
}

// SettleTokenPeriodQuota converts a reservation to actual usage. It may apply
// a negative correction for an over-estimate, then checks newly added output.
func SettleTokenPeriodQuota(tokenID int, reservedInput, actualInput, reservedOutput, actualOutput int64) error {
	if tokenID <= 0 {
		return nil
	}
	if reservedInput < 0 || actualInput < 0 || reservedOutput < 0 || actualOutput < 0 {
		return errors.New("token period quota settlement cannot be negative")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var token Token
		if err := lockForUpdate(tx).First(&token, tokenID).Error; err != nil {
			return err
		}
		resetTokenPeriodQuota(&token, time.Now())
		inputDelta := actualInput - reservedInput
		outputDelta := actualOutput - reservedOutput
		if inputDelta > 0 || outputDelta > 0 {
			if err := tokenQuotaCheck(&token, inputDelta, outputDelta); err != nil {
				return err
			}
		}
		applyTokenQuotaDelta(&token, inputDelta, outputDelta)
		if token.HourlyInputUsed < 0 || token.HourlyOutputUsed < 0 || token.DailyInputUsed < 0 || token.DailyOutputUsed < 0 || token.MonthlyInputUsed < 0 || token.MonthlyOutputUsed < 0 {
			return errors.New("token period quota reservation underflow")
		}
		return tx.Model(&token).Select("hourly_input_used", "hourly_output_used", "daily_input_used", "daily_output_used", "monthly_input_used", "monthly_output_used", "hourly_period_start", "daily_period_start", "monthly_period_start").Updates(&token).Error
	})
}

func ParseTokenQuotaInt(value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 0 {
		return 0, errors.New("token quota must be a non-negative integer")
	}
	return parsed, nil
}
