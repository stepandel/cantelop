package model

import (
	"errors"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/sandboxprofile"
)

const (
	MinSessionKeepAliveSeconds = 0
	MaxSessionKeepAliveSeconds = 7 * 24 * 60 * 60
)

var (
	ErrInvalidAppBuild          = errors.New("invalid app build")
	ErrAppBuildNotFound         = errors.New("App Build not found")
	ErrInvalidAppSessionRuntime = errors.New("invalid App Session runtime")
)

type AppBuild struct {
	AppID     string    `json:"app_id"`
	Version   int       `json:"version"`
	BuildID   string    `json:"build_id"`
	CreatedAt time.Time `json:"created_at"`
}

type AppRuntimeSpec struct {
	Profile      string `json:"profile"`
	InternalPort int    `json:"internal_port"`
}

type AppSessionRuntime struct {
	ActiveBuildID string         `json:"active_build_id,omitempty"`
	Runtime       AppRuntimeSpec `json:"runtime"`
}

func (b AppBuild) Validate() error {
	if ValidateResourceID(b.AppID) != nil || b.Version < 1 || b.CreatedAt.IsZero() {
		return ErrInvalidAppBuild
	}
	if err := ValidateBuildID(b.BuildID); err != nil {
		return errors.Join(ErrInvalidAppBuild, err)
	}
	return nil
}

func (h AppSessionRuntime) Validate() error {
	if h.ActiveBuildID != "" {
		if err := ValidateBuildID(h.ActiveBuildID); err != nil {
			return errors.Join(ErrInvalidAppSessionRuntime, err)
		}
	}
	if _, err := sandboxprofile.Resolve(h.Runtime.Profile); err != nil {
		return errors.Join(ErrInvalidAppSessionRuntime, err)
	}
	if h.Runtime.InternalPort < 1 || h.Runtime.InternalPort > 65535 {
		return ErrInvalidAppSessionRuntime
	}
	return nil
}

func (h AppSessionRuntime) WithDefaults() AppSessionRuntime {
	return h
}
