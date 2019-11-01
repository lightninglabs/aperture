package auth

import (
	"fmt"
	"strconv"
	"strings"
)

type Config struct {
	// LndHost is the hostname of the LND instance to connect to.
	LndHost string `long:"lndhost" description:"Hostname of the LND instance to connect to"`

	TlsPath string `long:"tlspath"`

	MacDir string `long:"macdir"`

	Network string `long:"network"`
}

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

func (l Level) FreebieCount() uint8 {
	parts := strings.Split(l.lower(), " ")
	if len(parts) != 2 {
		panic(fmt.Errorf("invalid auth value: %s", l.lower()))
	}
	count, err := strconv.Atoi(parts[1])
	if err != nil {
		panic(err)
	}
	return uint8(count)
}

func (l Level) IsOff() bool {
	lower := l.lower()
	return lower == "off" || lower == "false"
}
