package model

import (
	"errors"
	"regexp"
	"time"
)

type AppBuildOperationStatus string

const (
	AppBuildOperationReceiving  AppBuildOperationStatus = "receiving"
	AppBuildOperationQueued     AppBuildOperationStatus = "queued"
	AppBuildOperationProcessing AppBuildOperationStatus = "processing"
	AppBuildOperationSucceeded  AppBuildOperationStatus = "succeeded"
	AppBuildOperationFailed     AppBuildOperationStatus = "failed"
	DefaultMaxBuildsPerApp                              = 25
	DefaultRetainedBuildsPerApp                         = 20
	DefaultMaxRetainedBuilds                            = 250
)

var (
	ErrInvalidAppBuildOperation  = errors.New("invalid App Build operation")
	ErrAppBuildUploadPending     = errors.New("App already has a pending Build upload")
	ErrAppBuildQuota             = errors.New("App Build retention quota exceeded")
	ErrAppBuildOperationNotFound = errors.New("App Build operation not found")
	appBuildOperationIDPattern   = regexp.MustCompile(`^bop_[0-9a-f]{32}$`)
)

// AppBuildOperation is the durable state machine for one App-scoped archive.
// A successful operation is linked to the server-assigned Build version after
// materialization; customers never provide either a Build ID or version.
type AppBuildOperation struct {
	ID           string                  `json:"id"`
	AppID        string                  `json:"app_id"`
	Status       AppBuildOperationStatus `json:"status"`
	SizeBytes    int64                   `json:"size_bytes,omitempty"`
	BuildID      string                  `json:"build_id,omitempty"`
	BuildVersion int                     `json:"build_version,omitempty"`
	ErrorCode    string                  `json:"error_code,omitempty"`
	CreatedAt    time.Time               `json:"created_at"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

func (o AppBuildOperation) Validate() error {
	if !ValidAppBuildOperationID(o.ID) || ValidateResourceID(o.AppID) != nil ||
		o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) || o.SizeBytes < 0 {
		return ErrInvalidAppBuildOperation
	}
	switch o.Status {
	case AppBuildOperationReceiving:
		if o.SizeBytes != 0 || o.BuildID != "" || o.BuildVersion != 0 || o.ErrorCode != "" {
			return ErrInvalidAppBuildOperation
		}
	case AppBuildOperationQueued, AppBuildOperationProcessing:
		if o.SizeBytes < 1 || o.BuildID != "" || o.BuildVersion != 0 || o.ErrorCode != "" {
			return ErrInvalidAppBuildOperation
		}
	case AppBuildOperationSucceeded:
		if o.SizeBytes < 1 || ValidateBuildID(o.BuildID) != nil || o.BuildVersion < 1 || o.ErrorCode != "" {
			return ErrInvalidAppBuildOperation
		}
	case AppBuildOperationFailed:
		if o.BuildID != "" || o.BuildVersion != 0 || o.ErrorCode == "" {
			return ErrInvalidAppBuildOperation
		}
	default:
		return ErrInvalidAppBuildOperation
	}
	return nil
}

func ValidAppBuildOperationID(id string) bool { return appBuildOperationIDPattern.MatchString(id) }
