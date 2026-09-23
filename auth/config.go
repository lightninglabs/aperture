package auth

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/lightninglabs/aperture/freebie"
)

const (
	// LevelOff is the default level where no authentication is required.
	LevelOff Level = "off"
)

type Level string

// ParseLevel validates and normalizes an authentication level.
func ParseLevel(value string) (Level, error) {
	lower := strings.ToLower(value)

	switch {
	case lower == "" || lower == "on" || lower == "true" ||
		lower == "off" || lower == "false":

		return Level(lower), nil

	case strings.HasPrefix(lower, "freebie "):
		// The count is stored in a 16-bit freebie.Count, so parse it at
		// that size. A larger value would otherwise wrap around, and
		// "freebie 65536" would grant no free requests at all.
		parts := strings.SplitN(lower, " ", 2)
		count, err := strconv.ParseUint(parts[1], 10, 16)
		if err != nil || count == 0 {
			return "", fmt.Errorf("invalid freebie count, must be "+
				"between 1 and %d", math.MaxUint16)
		}

		return Level(lower), nil

	default:
		return "", fmt.Errorf("invalid auth level %q, must be 'on', "+
			"'off', or 'freebie N'", value)
	}
}

func (l Level) lower() string {
	return strings.ToLower(string(l))
}

func (l Level) IsOn() bool {
	lower := l.lower()
	return lower == "" || lower == "on" || lower == "true"
}

func (l Level) IsFreebie() bool {
	return strings.HasPrefix(l.lower(), "freebie ")
}

func (l Level) FreebieCount() freebie.Count {
	parts := strings.Split(l.lower(), " ")
	if len(parts) != 2 {
		panic(fmt.Errorf("invalid auth value: %s", l.lower()))
	}
	count, err := strconv.Atoi(parts[1])
	if err != nil {
		panic(err)
	}
	return freebie.Count(count)
}

func (l Level) IsOff() bool {
	lower := l.lower()
	return lower == "off" || lower == "false"
}
