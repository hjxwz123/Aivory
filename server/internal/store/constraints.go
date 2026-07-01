package store

import (
	"errors"
	"strings"
)

var (
	ErrChannelNameExists       = errors.New("name_exists")
	ErrOAuthProviderNameExists = errors.New("name_exists")
	ErrUserGroupNameExists     = errors.New("name_exists")
	ErrModelRequestExists      = errors.New("model_request_exists")
	ErrModelTagNameExists      = errors.New("name_exists")
	ErrImageStyleNameExists    = errors.New("name_exists")
	ErrProjectNameExists       = errors.New("name_exists")
	ErrKBNameExists            = errors.New("name_exists")
)

func isUniqueIndexErr(err error, indexNames ...string) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	for _, name := range indexNames {
		if strings.Contains(low, strings.ToLower(name)) {
			return true
		}
	}
	return false
}
