package hook

// BoundDiagnostic applies the dispatcher's configured output limit to text
// added by lifecycle adapters outside Dispatch itself.
func (d *Dispatcher) BoundDiagnostic(message string) string {
	if d == nil {
		return message
	}
	bounded, _ := boundedString(message, d.Config().MaxOutputBytes)
	return bounded
}

type boundedDiagnosticError struct {
	message string
	cause   error
}

func (e *boundedDiagnosticError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *boundedDiagnosticError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func boundErrorDiagnostic(err error, limit int) (error, int) {
	if err == nil {
		return nil, 0
	}
	message := err.Error()
	if limit <= 0 {
		if message == "" {
			return err, 0
		}
		return &boundedDiagnosticError{cause: err}, 0
	}
	bounded, truncated := boundedString(message, limit)
	if !truncated {
		return err, len(message)
	}
	return &boundedDiagnosticError{message: bounded, cause: err}, len(bounded)
}
