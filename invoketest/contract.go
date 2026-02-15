// Package invoketest provides a contract test suite for invoke providers.
package invoketest

// AllContracts returns all test cases for the contract test suite.
func AllContracts() []TestCase {
	const initialCapacity = 50

	contracts := make([]TestCase, 0, initialCapacity)

	contracts = append(contracts, coreContracts()...)
	contracts = append(contracts, environmentContracts()...)
	contracts = append(contracts, systemContracts()...)
	contracts = append(contracts, fileContracts()...)
	contracts = append(contracts, errorContracts()...)

	return contracts
}
