package jobs

// Domain handlers are also called directly by batch. Expose the same numeric
// value or null they would expose over JSON, rather than an internal optional
// pointer which an in-process consumer cannot recognize as a number.
func exitValue(code *int) any {
	if code == nil {
		return nil
	}
	return *code
}
