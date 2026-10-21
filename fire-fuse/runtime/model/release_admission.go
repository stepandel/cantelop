package model

import (
	"errors"
	"regexp"
)

var ErrReleaseAdmissionConflict = errors.New("App release admission generation conflict")
var releaseGenerationPattern = regexp.MustCompile(`^act_[0-9a-f]{32}$`)

type ReleaseAdmission struct {
	Generation string `json:"generation"`
	Closed     bool   `json:"closed"`
}

func ValidReleaseGeneration(generation string) bool {
	return releaseGenerationPattern.MatchString(generation)
}
