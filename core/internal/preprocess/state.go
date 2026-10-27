package preprocess

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"time"
)

const maxPeriod = 86400

// Memory holds per-item preprocessing state between collections.
type Memory struct {
	Value    float64
	At       time.Time
	LastEmit int64 // unix seconds; discard_unchanged_heartbeat
	LastKept int64 // unix seconds; throttle
}

// ErrNoBaseline means a delta step has nothing to compare with yet.
var ErrNoBaseline = errors.New("preprocess: no previous value")

// ErrThrottled means throttle dropped a value inside the period window.
var ErrThrottled = errors.New("preprocess: throttled")

// Validate checks step parameters that need bounds before collection runs.
func Validate(steps []Step) error {
	for _, st := range steps {
		kind := strings.ToLower(strings.TrimSpace(st.Type))
		switch kind {
		case "discard_unchanged_heartbeat", "throttle":
			if st.Seconds < 1 || st.Seconds > maxPeriod {
				return fmt.Errorf("%s: seconds must be from 1 to %d", kind, maxPeriod)
			}
		}
	}
	return nil
}

// ApplyAt runs steps with collection time and stored memory. Keep the returned
// memory even when the error is ErrNoBaseline, ErrUnchanged, or ErrThrottled.
func ApplyAt(steps []Step, input string, mem Memory, now time.Time) (float64, Memory, error) {
	text := input
	value := math.NaN()
	have := false
	nowUnix := now.Unix()
	for _, st := range steps {
		var err error
		switch strings.ToLower(strings.TrimSpace(st.Type)) {
		case "trim":
			text = strings.TrimSpace(text)
		case "ltrim":
			text = trimSide(text, st.Pattern, true)
		case "rtrim":
			text = trimSide(text, st.Pattern, false)
		case "replace":
			if st.Pattern == "" {
				return 0, mem, fmt.Errorf("replace: pattern required")
			}
			text = strings.ReplaceAll(text, st.Pattern, st.Replacement)
		case "regex":
			text, err = applyRegex(st, text)
			var syn *syntax.Error
			if errors.As(err, &syn) {
				return 0, mem, err
			}
		case "jsonpath":
			text, err = applyJSON(st.Path, text)
		case "xpath":
			text, err = applyXML(st.Path, text)
		case "multiplier":
			value, err = parseNum(text)
			if err == nil {
				factor := st.Factor
				if factor == 0 {
					factor = 1
				}
				value *= factor
				text = strconv.FormatFloat(value, 'f', -1, 64)
				have = true
			}
		case "bool":
			re, e := regexp.Compile(st.Pattern)
			if e != nil {
				return 0, mem, e
			}
			if re.MatchString(text) {
				value = 1
			} else {
				value = 0
			}
			text = strconv.FormatFloat(value, 'f', -1, 64)
			have = true
		case "matches":
			if err = mustMatch(st.Pattern, text, true); err != nil {
				return failStep(st, mem, err)
			}
		case "not_match":
			if err = mustMatch(st.Pattern, text, false); err != nil {
				return failStep(st, mem, err)
			}
		case "hex", "octal":
			value, err = parseBase(text, st.Type)
			if err != nil {
				return 0, mem, err
			}
			text = strconv.FormatInt(int64(value), 10)
			have = true
		case "in_range":
			value, err = parseNum(text)
			if err != nil {
				return 0, mem, err
			}
			if st.Min == nil && st.Max == nil {
				return 0, mem, fmt.Errorf("in_range: min or max required")
			}
			if st.Min != nil && value < *st.Min || st.Max != nil && value > *st.Max {
				return failStep(st, mem, fmt.Errorf("in_range: %v outside bounds", value))
			}
			text = strconv.FormatFloat(value, 'f', -1, 64)
			have = true
		case "valuemap":
			if len(st.Map) == 0 || len(st.Map) > 32 {
				return 0, mem, fmt.Errorf("valuemap: 1 to 32 entries required")
			}
			to, ok := st.Map[strings.TrimSpace(text)]
			if !ok {
				return 0, mem, fmt.Errorf("valuemap: %q is not mapped", strings.TrimSpace(text))
			}
			if len(to) > 128 {
				return 0, mem, fmt.Errorf("valuemap: replacement is too long")
			}
			text = to
		case "discard_unchanged", "discard_unchanged_heartbeat":
			heartbeat := strings.EqualFold(strings.TrimSpace(st.Type), "discard_unchanged_heartbeat")
			if heartbeat && (st.Seconds < 1 || st.Seconds > maxPeriod) {
				return 0, mem, fmt.Errorf("discard_unchanged_heartbeat: seconds must be from 1 to %d", maxPeriod)
			}
			value, err = parseNum(text)
			if err != nil {
				return 0, mem, err
			}
			if !math.IsNaN(mem.Value) && value == mem.Value {
				due := heartbeat && mem.LastEmit > 0 && nowUnix >= mem.LastEmit && nowUnix-mem.LastEmit >= int64(st.Seconds)
				if !due {
					return 0, mem, ErrUnchanged
				}
			}
			mem.Value = value
			if !now.IsZero() {
				mem.At = now
			}
			if heartbeat && nowUnix >= mem.LastEmit {
				mem.LastEmit = nowUnix
			}
			text = strconv.FormatFloat(value, 'f', -1, 64)
			have = true
		case "throttle":
			if st.Seconds < 1 || st.Seconds > maxPeriod {
				return 0, mem, fmt.Errorf("throttle: seconds must be from 1 to %d", maxPeriod)
			}
			value, err = parseNum(text)
			if err != nil {
				return 0, mem, err
			}
			if mem.LastKept > 0 && (nowUnix < mem.LastKept || nowUnix-mem.LastKept < int64(st.Seconds)) {
				return 0, mem, ErrThrottled
			}
			mem.Value = value
			if !now.IsZero() {
				mem.At = now
			}
			mem.LastKept = nowUnix
			text = strconv.FormatFloat(value, 'f', -1, 64)
			have = true
		case "error_json", "error_xml", "error_regex":
			msg, absent, err := payloadError(st, text)
			if err != nil {
				var syn *syntax.Error
				if errors.As(err, &syn) || strings.HasSuffix(err.Error(), "required") {
					return 0, mem, err
				}
				return failStep(st, mem, err)
			}
			if !absent {
				return failStep(st, mem, fmt.Errorf("%s: %s", strings.ToLower(strings.TrimSpace(st.Type)), clipStepError(msg)))
			}
		case "prometheus_to_json":
			text, err = applyPromJSON(st, text)
			if err != nil {
				return failStep(st, mem, err)
			}
			have = false
		case "not_supported":
			if strings.TrimSpace(st.Pattern) == "" || len(st.Pattern) > 256 {
				return 0, mem, fmt.Errorf("not_supported: pattern must be 1 to 256 bytes")
			}
			re, e := regexp.Compile(st.Pattern)
			if e != nil {
				return 0, mem, e
			}
			if re.MatchString(text) {
				msg := re.FindString(text)
				if msg == "" {
					msg = strings.TrimSpace(text)
				}
				runes := []rune(msg)
				if len(runes) > 128 {
					msg = string(runes[:128])
				}
				return failStep(st, mem, fmt.Errorf("%w: %s", ErrNotSupported, msg))
			}
		case "javascript", "js":
			text, err = applyJS(st.Script, text)
			if err != nil {
				return 0, mem, err
			}
			have = false
		case "change", "change_per_second":
			value, err = parseNum(text)
			if err != nil {
				return 0, mem, err
			}
			next := mem
			next.Value = value
			if !now.IsZero() {
				next.At = now
			}
			if math.IsNaN(mem.Value) {
				return failStep(st, next, fmt.Errorf("change: %w", ErrNoBaseline))
			}
			delta := value - mem.Value
			if strings.EqualFold(strings.TrimSpace(st.Type), "change") {
				return delta, next, nil
			}
			if now.IsZero() || mem.At.IsZero() || !now.After(mem.At) {
				return 0, next, fmt.Errorf("change_per_second: no elapsed time")
			}
			return delta / now.Sub(mem.At).Seconds(), next, nil
		default:
			return 0, mem, fmt.Errorf("unknown preprocess step %q", st.Type)
		}
		if err != nil {
			return failStep(st, mem, err)
		}
	}
	if !have {
		var err error
		value, err = parseNum(text)
		if err != nil {
			return failStep(Step{}, mem, err)
		}
	}
	return value, mem, nil
}
