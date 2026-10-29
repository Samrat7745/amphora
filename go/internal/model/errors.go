package model

import "fmt"

// Kind maps to the HTTP status the Spring RestControllerExceptionHandler picks.
type Kind int

const (
	KindInternal      Kind = iota // 500 (AmphoraServiceException, CastorServiceException, any other)
	KindNotFound                  // 404
	KindAlreadyExists             // 409
	KindBadRequest                // 400 with quoted message (IllegalArgumentException)
	KindUnauthorized              // 401
	KindBinding                   // 400 with empty body (Spring binding / Jackson errors)
)

// Error is a service error carrying its HTTP classification.
type Error struct {
	Kind Kind
	Msg  string
	// NullMsg renders the body as the JSON literal null (Java exception without message).
	NullMsg bool
	Cause   error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Cause }

func NotFound(format string, a ...any) *Error {
	return &Error{Kind: KindNotFound, Msg: fmt.Sprintf(format, a...)}
}
func AlreadyExists(msg string) *Error { return &Error{Kind: KindAlreadyExists, Msg: msg} }
func BadRequest(format string, a ...any) *Error {
	return &Error{Kind: KindBadRequest, Msg: fmt.Sprintf(format, a...)}
}
func Unauthorized(msg string) *Error { return &Error{Kind: KindUnauthorized, Msg: msg} }
func Internal(format string, a ...any) *Error {
	return &Error{Kind: KindInternal, Msg: fmt.Sprintf(format, a...)}
}

// Binding is a 400 without body.
func Binding(msg string) *Error { return &Error{Kind: KindBinding, Msg: msg} }

// NPE is a Java NullPointerException without message: 500 with body null.
func NPE() *Error { return &Error{Kind: KindInternal, NullMsg: true, Msg: "null"} }
