package coreutils

import (
	"testing"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/expand"
)

func withDateNow(t *testing.T, now time.Time) {
	t.Helper()
	prev := dateNow
	dateNow = func() time.Time {
		return now
	}
	t.Cleanup(func() {
		dateNow = prev
	})
}

func TestFormatPOSIXDateCModifierFallbacks(t *testing.T) {
	now := time.Date(2026, time.March, 18, 14, 5, 6, 0, time.UTC)

	got := formatPOSIXDate(now, "%Ec|%c|%Ex|%x|%EX|%X|%Od|%d|%Ob|%b|%OB|%B|%Oy|%y")
	want := "Wed Mar 18 14:05:06 2026|Wed Mar 18 14:05:06 2026|03/18/26|03/18/26|14:05:06|14:05:06|18|18|Mar|Mar|March|March|26|26"
	if got != want {
		t.Fatalf("formatPOSIXDate() = %q, want %q", got, want)
	}
}

func TestFormatPOSIXDatePaddingAndAliases(t *testing.T) {
	tests := []struct {
		name   string
		value  time.Time
		format string
		want   string
	}{
		{
			name:   "midnight",
			value:  time.Date(2026, time.January, 2, 0, 3, 4, 0, time.UTC),
			format: "%h|%b|%e|%I|%p|%r",
			want:   "Jan|Jan| 2|12|AM|12:03:04 AM",
		},
		{
			name:   "noon",
			value:  time.Date(2026, time.January, 2, 12, 3, 4, 0, time.UTC),
			format: "%h|%b|%e|%I|%p|%r",
			want:   "Jan|Jan| 2|12|PM|12:03:04 PM",
		},
		{
			name:   "afternoon",
			value:  time.Date(2026, time.January, 2, 13, 3, 4, 0, time.UTC),
			format: "%I|%p|%r",
			want:   "01|PM|01:03:04 PM",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatPOSIXDate(tt.value, tt.format); got != tt.want {
				t.Fatalf("formatPOSIXDate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatPOSIXDateLeapYearAndWeekBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		value  time.Time
		format string
		want   string
	}{
		{
			name:   "leap day",
			value:  time.Date(2024, time.February, 29, 0, 0, 0, 0, time.UTC),
			format: "%j",
			want:   "060",
		},
		{
			name:   "first day before first week",
			value:  time.Date(2021, time.January, 1, 0, 0, 0, 0, time.UTC),
			format: "%U|%W|%V|%G|%g",
			want:   "00|00|53|2020|20",
		},
		{
			name:   "first monday of iso week one",
			value:  time.Date(2021, time.January, 4, 0, 0, 0, 0, time.UTC),
			format: "%U|%W|%V|%G|%g",
			want:   "01|01|01|2021|21",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatPOSIXDate(tt.value, tt.format); got != tt.want {
				t.Fatalf("formatPOSIXDate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatPOSIXDateWideYearFlags(t *testing.T) {
	tests := []struct {
		name   string
		value  time.Time
		format string
		want   string
	}{
		{
			name:   "zero padded short year",
			value:  time.Date(27, time.January, 2, 0, 0, 0, 0, time.UTC),
			format: "%04Y|%010F|%02C",
			want:   "0027|0027-01-02|00",
		},
		{
			name:   "signed year when width exceeds threshold",
			value:  time.Date(270, time.January, 2, 0, 0, 0, 0, time.UTC),
			format: "%+5Y",
			want:   "+0270",
		},
		{
			name:   "extended year and date",
			value:  time.Date(12345, time.January, 2, 0, 0, 0, 0, time.UTC),
			format: "%+5Y|%05Y|%+12F",
			want:   "+12345|12345|+12345-01-02",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatPOSIXDate(tt.value, tt.format); got != tt.want {
				t.Fatalf("formatPOSIXDate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveDateLocationSupportsPOSIXTZRules(t *testing.T) {
	tests := []struct {
		name     string
		value    time.Time
		env      expand.Environ
		forceUTC bool
		want     string
	}{
		{
			name:  "utc0",
			value: time.Date(2026, time.March, 18, 14, 5, 6, 0, time.UTC),
			env:   expand.ListEnviron("TZ=UTC0"),
			want:  "2026-03-18 14:05:06 UTC +0000",
		},
		{
			name:  "winter standard time",
			value: time.Date(2026, time.January, 15, 15, 4, 5, 0, time.UTC),
			env:   expand.ListEnviron("TZ=EST5EDT,M3.2.0,M11.1.0"),
			want:  "2026-01-15 10:04:05 EST -0500",
		},
		{
			name:  "summer daylight time",
			value: time.Date(2026, time.July, 15, 14, 5, 6, 0, time.UTC),
			env:   expand.ListEnviron("TZ=EST5EDT,M3.2.0,M11.1.0"),
			want:  "2026-07-15 10:05:06 EDT -0400",
		},
		{
			name:     "force utc",
			value:    time.Date(2026, time.January, 15, 15, 4, 5, 0, time.UTC),
			env:      expand.ListEnviron("TZ=America/New_York"),
			forceUTC: true,
			want:     "2026-01-15 15:04:05 UTC +0000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			location, err := resolveDateLocation(tt.value, tt.env, tt.forceUTC)
			if err != nil {
				t.Fatalf("resolveDateLocation() error = %v", err)
			}
			got := tt.value.In(location).Format("2006-01-02 15:04:05 MST -0700")
			if got != tt.want {
				t.Fatalf("resolved time = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveDateLocationRejectsInvalidTimezones(t *testing.T) {
	_, err := resolveDateLocation(time.Date(2026, time.March, 18, 14, 5, 6, 0, time.UTC), expand.ListEnviron("TZ=Invalid/Zone"), false)
	if err == nil {
		t.Fatal("resolveDateLocation() error = nil, want invalid time zone")
	}
}
