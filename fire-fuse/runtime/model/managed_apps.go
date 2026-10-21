package model

import (
	"errors"
	"time"
)

var ErrManagedStateNotFound = errors.New("managed App state not found")

var ErrManagedAdmission = errors.New("managed App allowance or policy unavailable")

// Rate card v1: 1024 credits per allocated vCPU-second + MiB-second.
// Five seconds of every grant fund shutdown. Idle timers cannot extend grants.
type ManagedPolicy struct {
	Active          bool      `json:"active"`
	ExpiresAt       time.Time `json:"expiresAt"`
	MaxSandboxes    int       `json:"maxSandboxes"`
	MaxVcpus        int       `json:"maxVcpus"`
	MaxMemoryMiB    int       `json:"maxMemoryMib"`
	MaxWorkspaces   int       `json:"maxWorkspaces"`
	MaxStorageBytes int64     `json:"maxStorageBytes"`
}
type ManagedGrant struct {
	HostID    string    `json:"hostId"`
	ID        string    `json:"id"`
	Epoch     string    `json:"epoch"`
	Credits   int64     `json:"credits"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type ManagedProjection struct {
	Draining bool          `json:"draining"`
	Revision int64         `json:"revision"`
	Policy   ManagedPolicy `json:"policy"`
	Grant    ManagedGrant  `json:"grant"`
}
type ManagedState struct {
	ManagedProjection
	Consumed  int64     `json:"consumed"`
	LastTick  time.Time `json:"lastTick"`
	Rate      int64     `json:"rate"`
	Remainder int64     `json:"remainder"`
}
type ManagedUsage struct {
	HostID  string `json:"hostId"`
	ID      string `json:"id"`
	Credits int64  `json:"credits"`
	Ack     bool   `json:"ack"`
}
