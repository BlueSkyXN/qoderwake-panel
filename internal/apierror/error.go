// Package apierror defines the stable error contract shared by the Panel APIs.
package apierror

import "net/http"

type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string          { return e.Code }
func New(status int, code string) error { return &Error{status, code} }
func Invalid(code string) error         { return New(http.StatusBadRequest, code) }
