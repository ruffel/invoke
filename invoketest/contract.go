// Package invoketest provides a contract test suite for invoke providers.
package invoketest

import (
	"fmt"
	"strings"
)

// AllContracts returns all test cases for the contract test suite.
func AllContracts() []TestCase {
	const initialCapacity = 50

	contracts := make([]TestCase, 0, initialCapacity)

	contracts = append(contracts, coreContracts()...)
	contracts = append(contracts, systemContracts()...)
	contracts = append(contracts, fileContracts()...)
	contracts = append(contracts, errorContracts()...)
	contracts = append(contracts, environmentContracts()...)

	validateContracts(contracts)

	return contracts
}

func validateContracts(contracts []TestCase) {
	seen := make(map[string]struct{}, len(contracts))

	for _, tc := range contracts {
		if strings.TrimSpace(tc.Category) == "" {
			panic("invoketest: contract category must not be empty")
		}

		if strings.TrimSpace(tc.Name) == "" {
			panic(fmt.Sprintf("invoketest: contract name must not be empty (category=%q)", tc.Category))
		}

		if tc.Run == nil {
			panic(fmt.Sprintf("invoketest: contract run func must not be nil (id=%q)", tc.ID()))
		}

		id := tc.ID()
		if _, exists := seen[id]; exists {
			panic(fmt.Sprintf("invoketest: duplicate contract id %q", id))
		}

		seen[id] = struct{}{}
	}
}
