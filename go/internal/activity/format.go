package activity

import (
	"fmt"
	"strconv"
	"strings"
)

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func escape(v any) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;").Replace(stringOf(v))
}

func humanDuration(v float64) string {
	if v < 1000 {
		return stringOf(v) + " ms"
	}
	if v < 60000 {
		digits := 0
		if v < 10000 {
			digits = 1
		}
		return strconv.FormatFloat(v/1000, 'f', digits, 64) + " s"
	}
	return strconv.FormatFloat(v/60000, 'f', 1, 64) + " min"
}

func plural(v float64, singular string, alternate ...string) string {
	word := singular
	if v != 1 {
		if len(alternate) > 0 {
			word = alternate[0]
		} else {
			word += "s"
		}
	}
	return stringOf(v) + " " + word
}
