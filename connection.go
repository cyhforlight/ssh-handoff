package main

import (
	"errors"
	"fmt"
	"strings"
)

type connectionSpec struct {
	Profile  string `json:"profile,omitzero"`
	Host     string `json:"host,omitzero"`
	User     string `json:"user,omitzero"`
	Port     int    `json:"port,omitzero"`
	Identity string `json:"identity,omitzero"`
}

func (connection connectionSpec) validate() error {
	if connection.Profile != "" {
		switch {
		case connection.Host != "" || connection.User != "" ||
			connection.Port != 0 || connection.Identity != "":
			return errors.New("profile cannot be combined with direct connection fields")
		case strings.ContainsRune(connection.Profile, 0):
			return errors.New("profile must not contain NUL")
		case strings.HasPrefix(connection.Profile, "-"):
			return errors.New("profile must not start with '-'")
		default:
			return nil
		}
	}

	switch {
	case connection.Host == "":
		return errors.New("host is required")
	case connection.User == "":
		return errors.New("user is required")
	case connection.Port < 1 || connection.Port > 65535:
		return fmt.Errorf("port must be between 1 and 65535: %d", connection.Port)
	case strings.ContainsRune(connection.Host, 0):
		return errors.New("host must not contain NUL")
	case strings.HasPrefix(connection.Host, "-"):
		return errors.New("host must not start with '-'")
	case strings.ContainsRune(connection.User, 0):
		return errors.New("user must not contain NUL")
	case strings.ContainsRune(connection.Identity, 0):
		return errors.New("identity must not contain NUL")
	default:
		return nil
	}
}

func (connection connectionSpec) targetLabel() string {
	if connection.Profile != "" {
		return "profile " + connection.Profile
	}
	host := connection.Host
	if strings.ContainsRune(host, ':') &&
		(!strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]")) {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s@%s:%d", connection.User, host, connection.Port)
}

func (connection connectionSpec) label() string {
	label := connection.targetLabel()
	if connection.Identity != "" {
		label += " identity=" + connection.Identity
	}
	return label
}
