package main

import (
	"errors"
	"fmt"
)

func validatePagination(limit, offset, maxLimit int) error {
	if limit < 1 || limit > maxLimit {
		return usageError(fmt.Errorf("--limit must be between 1 and %d", maxLimit))
	}
	if offset < 0 {
		return usageError(errors.New("--offset must not be negative"))
	}
	return nil
}
