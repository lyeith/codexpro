package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type operationalSpec struct {
	Key, Field         string
	Fallback, Min, Max int
}

var operationalLimits = []operationalSpec{
	{"CODEXPRO_MAX_OUTPUT_BYTES", "maxOutputBytes", 120000, 4000, 2000000},
	{"CODEXPRO_MAX_BASH_TIMEOUT_MS", "maxBashTimeoutMs", 600000, 1000, 900000},
	{"CODEXPRO_BASH_TIMEOUT_MS", "bashTimeoutMs", 120000, 1000, 900000},
	{"CODEXPRO_JOB_TIMEOUT_MS", "jobTimeoutMs", 2700000, 10000, 21600000},
	{"CODEXPRO_MAX_JOBS", "maxJobs", 12, 1, 64},
	{"CODEXPRO_MAX_JOBS_PER_WORKSPACE", "maxJobsPerWorkspace", 6, 1, 32},
	{"CODEXPRO_MAX_JOB_OUTPUT_BYTES", "maxJobOutputBytes", 8388608, 65536, 268435456},
	{"CODEXPRO_JOB_RETENTION_MS", "jobRetentionMs", 86400000, 1000, 604800000},
	{"CODEXPRO_MAX_RETAINED_JOB_BYTES", "maxRetainedJobBytes", 536870912, 65536, 2147483648},
	{"CODEXPRO_MAX_JOB_HISTORY_PER_WORKSPACE", "maxJobHistoryPerWorkspace", 50, 1, 200},
	{"CODEXPRO_AUDIT_MAX_BYTES", "auditMaxBytes", 8388608, 4096, 1073741824},
	{"CODEXPRO_AUDIT_RETAIN_ACTIONS", "auditRetainActions", 350, 1, 1000000},
}

type environmentValues struct {
	Values map[string]string
	Keys   []string
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func parseEnvironmentFile(text string) (environmentValues, error) {
	out := environmentValues{Values: map[string]string{}, Keys: []string{}}
	if !utf8.ValidString(text) {
		return out, errors.New("cannot read or decode EnvironmentFile")
	}
	if strings.ContainsAny(text, "\x00\ufeff") {
		return out, errors.New("invalid EnvironmentFile encoding")
	}
	chars := []rune(text)
	i := 0
	horizontal := func() {
		for i < len(chars) && (chars[i] == ' ' || chars[i] == '\t' || chars[i] == '\r') {
			i++
		}
	}
	for i < len(chars) {
		horizontal()
		if i >= len(chars) {
			break
		}
		if chars[i] == '\n' {
			i++
			continue
		}
		start := i
		for i < len(chars) && chars[i] != '\n' && chars[i] != '=' {
			i++
		}
		if chars[start] == '#' || chars[start] == ';' || i >= len(chars) || chars[i] != '=' {
			for i < len(chars) && chars[i] != '\n' {
				i++
			}
			continue
		}
		key := strings.TrimSpace(string(chars[start:i]))
		if !environmentName.MatchString(key) {
			return out, errors.New("invalid EnvironmentFile assignment name")
		}
		i++
		horizontal()
		value := []rune{}
		quote := rune(0)
		if i < len(chars) && (chars[i] == '\'' || chars[i] == '"') {
			quote = chars[i]
			i++
		}
		if quote != 0 {
			for i < len(chars) && chars[i] != quote {
				char := chars[i]
				i++
				if quote == '"' && char == '\\' && i < len(chars) {
					next := chars[i]
					i++
					if next == '\n' {
						continue
					}
					if next == '"' || next == '\\' || next == '$' || next == 96 {
						value = append(value, next)
					} else {
						value = append(value, '\\', next)
					}
				} else {
					value = append(value, char)
				}
			}
			if i >= len(chars) || chars[i] != quote {
				return out, errors.New("unterminated EnvironmentFile quote")
			}
			i++
			horizontal()
			if i < len(chars) && chars[i] != '\n' {
				return out, errors.New("invalid text after EnvironmentFile quote")
			}
		} else {
			for i < len(chars) && chars[i] != '\n' {
				char := chars[i]
				i++
				if char == '\\' && i < len(chars) {
					next := chars[i]
					i++
					if next != '\n' {
						value = append(value, next)
					}
				} else {
					value = append(value, char)
				}
			}
			value = []rune(strings.TrimRightFunc(string(value), unicode.IsSpace))
		}
		if _, exists := out.Values[key]; !exists {
			out.Keys = append(out.Keys, key)
		}
		out.Values[key] = string(value)
	}
	return out, nil
}
func readEnvironment(path string) (environmentValues, error) {
	bytes, e := readBounded(path, 1024*1024)
	if e != nil {
		if os.IsNotExist(e) {
			return environmentValues{}, os.ErrNotExist
		}
		if strings.Contains(e.Error(), "exceeds") {
			return environmentValues{}, errors.New("EnvironmentFile exceeds 1 MiB")
		}
		return environmentValues{}, errors.New("cannot read or decode EnvironmentFile")
	}
	return parseEnvironmentFile(string(bytes))
}

func startupNumber(value string, present bool, fallback, minimum, maximum int) int {
	if !present {
		return fallback
	}
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return min(max(number, minimum), maximum)
}

// drainTimeoutMs bounds the wait for in-flight tool calls after SIGTERM.
func drainTimeoutMs(value string, present bool) int {
	if !present {
		return 30000
	}
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number < 0 {
		return 30000
	}
	return min(number, 600000)
}

func booleanValue(value string, present bool, fallback bool) bool {
	if !present {
		return fallback
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}

const accessLogKey = "CODEXPRO_ACCESS_LOG"

func parseAccessLog(value string, present bool) (bool, error) {
	if !present {
		return false, nil
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", accessLogKey)
	}
	return enabled, nil
}

type ReloadReport struct {
	OK      bool     `json:"ok"`
	Changed []string `json:"changed"`
	Ignored []string `json:"ignored"`
}

// Reload changes the operational limits and access logging. Missing keys retain
// their values; unknown/startup keys are ignored without exposing their values.
func (c *Config) Reload() error { _, e := c.ReloadOperationalLimits(); return e }
func (c *Config) ReloadOperationalLimits() (ReloadReport, error) {
	report := ReloadReport{Changed: []string{}, Ignored: []string{}}
	parsed, e := readEnvironment(c.EnvFile)
	if e != nil {
		return report, e
	}
	known := map[string]bool{accessLogKey: true}
	for _, spec := range operationalLimits {
		known[spec.Key] = true
	}
	for _, key := range parsed.Keys {
		if !known[key] {
			report.Ignored = append(report.Ignored, key)
		}
	}
	updates := map[string]int{}
	for _, spec := range operationalLimits {
		value, present := parsed.Values[spec.Key]
		if !present {
			continue
		}
		numeric, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || numeric < spec.Min || numeric > spec.Max {
			return report, fmt.Errorf("%s must be an integer between %d and %d", spec.Key, spec.Min, spec.Max)
		}
		updates[spec.Field] = numeric
	}
	accessValue, accessPresent := parsed.Values[accessLogKey]
	accessLog, err := parseAccessLog(accessValue, accessPresent)
	if err != nil {
		return report, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = defaults()
	}
	for _, spec := range operationalLimits {
		if value, present := updates[spec.Field]; present {
			before := numericValue(c.values[spec.Field], spec.Fallback)
			if before != value {
				report.Changed = append(report.Changed, fmt.Sprintf("%s: %d -> %d", spec.Key, before, value))
			}
			c.values[spec.Field] = value
		}
	}
	if accessPresent {
		before, _ := c.values["accessLog"].(bool)
		if before != accessLog {
			report.Changed = append(report.Changed, fmt.Sprintf("%s: %t -> %t", accessLogKey, before, accessLog))
		}
		c.values["accessLog"] = accessLog
	}
	report.OK = true
	return report, nil
}
func numericValue(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return fallback
}
