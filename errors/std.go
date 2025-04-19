package errors

import ge "errors"

func Is(err, target error) bool {
	return ge.Is(err, target)
}

func As(err error, target interface{}) bool {
	return ge.As(err, target)
}

// Unwrap returns the result of calling the Unwrap method on err, if err's
// type contains an Unwrap method returning error.
// Otherwise, Unwrap returns nil.
//
// Unwrap only calls a method of the form "Unwrap() error".
// In particular Unwrap does not unwrap errors returned by [Join].
func Unwrap(err error) error {
	u, ok := err.(interface {
		Unwrap() error
	})
	if !ok {
		return nil
	}
	return u.Unwrap()
}

func Join(errs ...error) error {
	return ge.Join(errs...)
}
