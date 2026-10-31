package wire

import "fmt"

// NativeError preserves an explicitly known source exception name without
// turning it into a CodexProError or inventing domain metadata. Ordinary native
// errors keep ErrorResult's default Error name.
type NativeError struct {
	Name    string
	Message string
}

func (e *NativeError) Error() string { return e.Message }

func NativeErrorf(name, format string, args ...any) error {
	return &NativeError{Name: name, Message: fmt.Sprintf(format, args...)}
}
