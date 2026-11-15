package service

import "github.com/liulixin-lex/xy2api/internal/resilience"

// HTTPContinuationError carries an existing HTTP continuation rejection.
type HTTPContinuationError struct {
	Status               int
	Code, Message, Param string
	Family               resilience.StateFamily
}

func (e *HTTPContinuationError) Error() string { return e.Message }
