package api

import "errors"

var (
	errAuthRequired   = errors.New("auth required")
	errAccountBlocked = errors.New("account disabled")
	errSessionExpired = errors.New("session expired, please log in again")
	errAdminOnly      = errors.New("admin only")
	errInvalidInput   = errors.New("invalid input")
	errNotFound       = errors.New("not found")
)
