package microvm

import (
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

const (
	MinSessionKeepAliveSeconds = model.MinSessionKeepAliveSeconds
	MaxSessionKeepAliveSeconds = model.MaxSessionKeepAliveSeconds
)

var (
	ErrInvalidAppBuild          = model.ErrInvalidAppBuild
	ErrAppBuildNotFound         = model.ErrAppBuildNotFound
	ErrInvalidAppSessionRuntime = model.ErrInvalidAppSessionRuntime
)

type AppBuild = model.AppBuild
type AppRuntimeSpec = model.AppRuntimeSpec
type AppSessionRuntime = model.AppSessionRuntime
