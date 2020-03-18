package auth

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lightninglabs/aperture/freebie"
)

const (
	// LevelOff is the default level where no authentication is required.
	LevelOff Level = "off"
)

type Level string

func (l Level) lower() string {
	return strings.ToLower(string(l))
}

func (l Level) IsOn() bool {
	lower := l.lower()
	return lower == "" || lower == "on" || lower == "true"
}

func (l Level) IsFreebie() bool {
	return strings.HasPrefix(l.lower(), "freebie")
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
