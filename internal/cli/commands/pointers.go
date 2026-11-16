package commands

func valueOr[T any](v *T, fallback T) T {
	if v != nil {
		return *v
	}
	return fallback
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
