package main

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")). // Pink
			MarginTop(1).
			MarginBottom(1)

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("86")). // Cyan
			MarginLeft(2)

	errorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("196")). // Red
			MarginTop(1)

	stepStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12")). // Blue
			MarginTop(1)

	checkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("46")). // Green
			MarginTop(1)
)
