package jqgo

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Broken-down time is jq's array form:
// [year, month (0-11), day of month, hours, minutes, seconds, weekday, day of year (0-365)]

func nowFloat() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

func timeFromNumber(f float64, local bool) time.Time {
	sec, frac := math.Modf(f)
	t := time.Unix(int64(sec), int64(frac*1e9))
	if local {
		return t.Local()
	}
	return t.UTC()
}

func brokenDownOf(t time.Time, fracSec float64) []any {
	sec := any(t.Second())
	if fracSec != 0 {
		sec = float64(t.Second()) + fracSec
	}
	return []any{t.Year(), int(t.Month()) - 1, t.Day(), t.Hour(), t.Minute(), sec, int(t.Weekday()), t.YearDay() - 1}
}

func brokenDown(v any, local bool) (any, error) {
	f, ok := toFloat(v)
	if !ok {
		name := "gmtime"
		if local {
			name = "localtime"
		}
		return nil, fmt.Errorf("%s() requires a number", name)
	}
	t := timeFromNumber(f, local)
	_, frac := math.Modf(f)
	if frac < 0 {
		frac += 1
	}
	return brokenDownOf(t, frac), nil
}

// timeFromBrokenDown interprets a broken-down time array as UTC (timegm).
func timeFromBrokenDown(v any, fname string) (time.Time, error) {
	arr, ok := v.([]any)
	if !ok || len(arr) < 6 {
		return time.Time{}, fmt.Errorf("%s requires array of 6 numbers", fname)
	}
	var n [6]float64
	for i := 0; i < 6; i++ {
		f, ok := toFloat(arr[i])
		if !ok {
			return time.Time{}, fmt.Errorf("%s requires parsed datetime inputs", fname)
		}
		n[i] = f
	}
	sec, frac := math.Modf(n[5])
	return time.Date(int(n[0]), time.Month(int(n[1])+1), int(n[2]), int(n[3]), int(n[4]), int(sec), int(frac*1e9), time.UTC), nil
}

func mktime(e *evaluator, v any, _ []any) (any, error) {
	if _, ok := v.([]any); !ok {
		return nil, fmt.Errorf("mktime requires array of 6 numbers")
	}
	t, err := timeFromBrokenDown(v, "mktime")
	if err != nil {
		return nil, err
	}
	return int(t.Unix()), nil
}

func strftimeFn(v, format any, local bool) (any, error) {
	name := "strftime/1"
	if local {
		name = "strflocaltime/1"
	}
	fs, ok := format.(string)
	if !ok {
		return nil, fmt.Errorf("%s requires a string format", name)
	}
	var t time.Time
	switch x := v.(type) {
	case int, float64:
		f, _ := toFloat(x)
		t = timeFromNumber(f, local)
	case []any:
		var err error
		if t, err = timeFromBrokenDown(x, name); err != nil {
			return nil, err
		}
		if local {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.Local)
		}
	default:
		return nil, fmt.Errorf("%s requires parsed datetime inputs", name)
	}
	return strftime(t, fs), nil
}

var weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
var monthNames = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}

func strftime(t time.Time, f string) string {
	var sb strings.Builder
	pad := func(n, width int, fill byte) {
		s := strconv.Itoa(n)
		for i := len(s); i < width; i++ {
			sb.WriteByte(fill)
		}
		sb.WriteString(s)
	}
	for i := 0; i < len(f); i++ {
		c := f[i]
		if c != '%' || i+1 >= len(f) {
			sb.WriteByte(c)
			continue
		}
		i++
		switch f[i] {
		case 'a':
			sb.WriteString(weekdayNames[t.Weekday()][:3])
		case 'A':
			sb.WriteString(weekdayNames[t.Weekday()])
		case 'b', 'h':
			sb.WriteString(monthNames[t.Month()-1][:3])
		case 'B':
			sb.WriteString(monthNames[t.Month()-1])
		case 'c':
			sb.WriteString(strftime(t, "%a %b %e %H:%M:%S %Y"))
		case 'C':
			pad(t.Year()/100, 2, '0')
		case 'd':
			pad(t.Day(), 2, '0')
		case 'D':
			sb.WriteString(strftime(t, "%m/%d/%y"))
		case 'e':
			pad(t.Day(), 2, ' ')
		case 'F':
			sb.WriteString(strftime(t, "%Y-%m-%d"))
		case 'g':
			y, _ := t.ISOWeek()
			pad(y%100, 2, '0')
		case 'G':
			y, _ := t.ISOWeek()
			pad(y, 4, '0')
		case 'H':
			pad(t.Hour(), 2, '0')
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			pad(h, 2, '0')
		case 'j':
			pad(t.YearDay(), 3, '0')
		case 'k':
			pad(t.Hour(), 2, ' ')
		case 'l':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			pad(h, 2, ' ')
		case 'm':
			pad(int(t.Month()), 2, '0')
		case 'M':
			pad(t.Minute(), 2, '0')
		case 'n':
			sb.WriteByte('\n')
		case 'p':
			if t.Hour() < 12 {
				sb.WriteString("AM")
			} else {
				sb.WriteString("PM")
			}
		case 'P':
			if t.Hour() < 12 {
				sb.WriteString("am")
			} else {
				sb.WriteString("pm")
			}
		case 'r':
			sb.WriteString(strftime(t, "%I:%M:%S %p"))
		case 'R':
			sb.WriteString(strftime(t, "%H:%M"))
		case 's':
			sb.WriteString(strconv.FormatInt(t.Unix(), 10))
		case 'S':
			pad(t.Second(), 2, '0')
		case 't':
			sb.WriteByte('\t')
		case 'T':
			sb.WriteString(strftime(t, "%H:%M:%S"))
		case 'u':
			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7
			}
			pad(wd, 1, '0')
		case 'U':
			pad((t.YearDay()+6-int(t.Weekday()))/7, 2, '0')
		case 'V':
			_, w := t.ISOWeek()
			pad(w, 2, '0')
		case 'w':
			pad(int(t.Weekday()), 1, '0')
		case 'W':
			pad((t.YearDay()+6-(int(t.Weekday())+6)%7)/7, 2, '0')
		case 'x':
			sb.WriteString(strftime(t, "%m/%d/%y"))
		case 'X':
			sb.WriteString(strftime(t, "%H:%M:%S"))
		case 'y':
			pad(t.Year()%100, 2, '0')
		case 'Y':
			sb.WriteString(strconv.Itoa(t.Year()))
		case 'z':
			sb.WriteString(t.Format("-0700"))
		case 'Z':
			name, _ := t.Zone()
			sb.WriteString(name)
		case '%':
			sb.WriteByte('%')
		default:
			sb.WriteByte('%')
			sb.WriteByte(f[i])
		}
	}
	return sb.String()
}

func strptimeFn(e *evaluator, v any, args []any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("strptime/1 requires string inputs and arguments")
	}
	f, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("strptime/1 requires string inputs and arguments")
	}
	t, err := strptime(s, f)
	if err != nil {
		return nil, err
	}
	return brokenDownOf(t, 0), nil
}

func strptime(s, f string) (time.Time, error) {
	year, month, day, hour, min, sec := 1900, 1, 1, 0, 0, 0
	yday := -1
	pm, hasPM, epoch := false, false, int64(-1)
	fail := func() (time.Time, error) {
		return time.Time{}, fmt.Errorf("date \"%s\" does not match format \"%s\"", s, f)
	}
	si := 0
	num := func(maxDigits int) (int, bool) {
		start := si
		neg := false
		if si < len(s) && (s[si] == '+' || s[si] == '-') {
			neg = s[si] == '-'
			si++
		}
		ds := si
		for si < len(s) && si-ds < maxDigits && s[si] >= '0' && s[si] <= '9' {
			si++
		}
		if si == ds {
			si = start
			return 0, false
		}
		n, _ := strconv.Atoi(s[ds:si])
		if neg {
			n = -n
		}
		return n, true
	}
	name := func(names []string) (int, bool) {
		for i, full := range names {
			for _, cand := range []string{full, full[:3]} {
				if len(s)-si >= len(cand) && strings.EqualFold(s[si:si+len(cand)], cand) {
					si += len(cand)
					return i, true
				}
			}
		}
		return 0, false
	}
	var parse func(f string) bool
	parse = func(f string) bool {
		for i := 0; i < len(f); i++ {
			c := f[i]
			if c == ' ' || c == '\t' || c == '\n' {
				for si < len(s) && (s[si] == ' ' || s[si] == '\t' || s[si] == '\n') {
					si++
				}
				continue
			}
			if c != '%' || i+1 >= len(f) {
				if si >= len(s) || s[si] != c {
					return false
				}
				si++
				continue
			}
			i++
			var ok bool
			switch f[i] {
			case 'Y':
				year, ok = num(4)
			case 'm':
				month, ok = num(2)
			case 'd', 'e':
				for si < len(s) && s[si] == ' ' {
					si++
				}
				day, ok = num(2)
			case 'H', 'k':
				for si < len(s) && s[si] == ' ' {
					si++
				}
				hour, ok = num(2)
			case 'I', 'l':
				for si < len(s) && s[si] == ' ' {
					si++
				}
				hour, ok = num(2)
				if hour == 12 {
					hour = 0
				}
			case 'M':
				min, ok = num(2)
			case 'S':
				sec, ok = num(2)
			case 'j':
				yday, ok = num(3)
			case 'y':
				var y int
				y, ok = num(2)
				if y < 69 {
					year = 2000 + y
				} else {
					year = 1900 + y
				}
			case 'C':
				var c int
				c, ok = num(2)
				year = c*100 + year%100
			case 'b', 'B', 'h':
				var m int
				m, ok = name(monthNames)
				month = m + 1
			case 'a', 'A':
				_, ok = name(weekdayNames)
			case 'u', 'w':
				_, ok = num(1)
			case 'p', 'P':
				switch {
				case len(s)-si >= 2 && strings.EqualFold(s[si:si+2], "AM"):
					ok, hasPM = true, true
				case len(s)-si >= 2 && strings.EqualFold(s[si:si+2], "PM"):
					ok, hasPM, pm = true, true, true
				}
				si += 2
			case 'z':
				if si < len(s) && (s[si] == 'Z' || s[si] == 'z') {
					si++
					ok = true
				} else {
					_, ok = num(4)
					if ok && si < len(s) && s[si] == ':' {
						si++
						_, ok = num(2)
					}
				}
			case 'Z':
				for si < len(s) && ((s[si] >= 'A' && s[si] <= 'Z') || (s[si] >= 'a' && s[si] <= 'z')) {
					si++
				}
				ok = true
			case 's':
				var n int
				n, ok = num(20)
				epoch = int64(n)
			case 'T':
				ok = parse("%H:%M:%S")
			case 'D':
				ok = parse("%m/%d/%y")
			case 'F':
				ok = parse("%Y-%m-%d")
			case 'R':
				ok = parse("%H:%M")
			case 'r':
				ok = parse("%I:%M:%S %p")
			case 'c':
				ok = parse("%a %b %e %H:%M:%S %Y")
			case 'n', 't':
				for si < len(s) && (s[si] == ' ' || s[si] == '\t' || s[si] == '\n') {
					si++
				}
				ok = true
			case '%':
				ok = si < len(s) && s[si] == '%'
				si++
			default:
				return false
			}
			if !ok {
				return false
			}
		}
		return true
	}
	if !parse(f) || si != len(s) {
		return fail()
	}
	if epoch >= 0 {
		return time.Unix(epoch, 0).UTC(), nil
	}
	if hasPM && pm {
		hour += 12
	}
	if yday >= 0 && month == 1 && day == 1 {
		return time.Date(year, 1, yday, hour, min, sec, 0, time.UTC), nil
	}
	return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC), nil
}

func registerTime() {
	defFn("now", 0, func(*evaluator, any, []any) (any, error) { return nowFloat(), nil })
	defFn("mktime", 0, mktime)
	defFn("gmtime", 0, func(e *evaluator, v any, _ []any) (any, error) { return brokenDown(v, false) })
	defFn("localtime", 0, func(e *evaluator, v any, _ []any) (any, error) { return brokenDown(v, true) })
	defFn("strftime", 1, func(e *evaluator, v any, args []any) (any, error) { return strftimeFn(v, args[0], false) })
	defFn("strflocaltime", 1, func(e *evaluator, v any, args []any) (any, error) { return strftimeFn(v, args[0], true) })
	defFn("strptime", 1, strptimeFn)
}
