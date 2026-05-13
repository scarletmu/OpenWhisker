package executor

import (
	"errors"
	"fmt"
)

type ConflictError struct {
	Message string
}

func (e ConflictError) Error() string {
	return e.Message
}

func IsConflict(err error) bool {
	var conflict ConflictError
	return errors.As(err, &conflict)
}

func conflictError(format string, args ...any) error {
	return ConflictError{Message: fmt.Sprintf(format, args...)}
}
