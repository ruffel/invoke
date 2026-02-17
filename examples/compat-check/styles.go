package main

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("33")). // Blue
			MarginTop(1).
			MarginBottom(1)

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")). // Gray
			MarginLeft(2)

	errorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("160")). // Red
			MarginTop(1)

	checkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("40")). // Green
			MarginTop(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("255")).
			Background(lipgloss.Color("33")).
			Padding(0, 1)

	rowStyle = lipgloss.NewStyle().
			Padding(0, 1)

	passedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("40"))

	failedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("160"))

	skippedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	parityMatchStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("40")).
				Bold(true)

	parityDivergedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("160")).
				Bold(true)

	parityNAStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true)

	catStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("33")).
			Bold(true).
			MarginTop(1)
)
