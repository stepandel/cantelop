package microvm

import "github.com/stepandel/cantelop/fire-fuse/runtime/model"

type AppBuildOperationStatus = model.AppBuildOperationStatus

const (
	AppBuildOperationReceiving  = model.AppBuildOperationReceiving
	AppBuildOperationQueued     = model.AppBuildOperationQueued
	AppBuildOperationProcessing = model.AppBuildOperationProcessing
	AppBuildOperationSucceeded  = model.AppBuildOperationSucceeded
	AppBuildOperationFailed     = model.AppBuildOperationFailed
	DefaultMaxBuildsPerApp      = model.DefaultMaxBuildsPerApp
	DefaultMaxRetainedBuilds    = model.DefaultMaxRetainedBuilds
)

var (
	ErrInvalidAppBuildOperation  = model.ErrInvalidAppBuildOperation
	ErrAppBuildUploadPending     = model.ErrAppBuildUploadPending
	ErrAppBuildQuota             = model.ErrAppBuildQuota
	ErrAppBuildOperationNotFound = model.ErrAppBuildOperationNotFound
)

type AppBuildOperation = model.AppBuildOperation

func ValidAppBuildOperationID(id string) bool { return model.ValidAppBuildOperationID(id) }
