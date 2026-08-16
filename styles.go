package main

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	ColorPrimary = lipgloss.Color("205")
	ColorError   = lipgloss.Color("197")
	ColorMuted   = lipgloss.Color("240")
	ColorBasic   = lipgloss.Color("250")
	ColorActive  = lipgloss.Color("76")

	// Styles

	ViewStyle = lipgloss.NewStyle().
			MarginTop(1).
			PaddingTop(1).
			PaddingLeft(2).
			PaddingRight(2).
			PaddingBottom(1).
			MarginBottom(1).
			Border(lipgloss.RoundedBorder(), true).
			BorderForeground(ColorPrimary)

	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary)

	MetaStyle = lipgloss.NewStyle().Foreground(ColorMuted)

	CountStyle = lipgloss.NewStyle().Foreground(ColorActive)

	DescStyle = lipgloss.NewStyle().
			Foreground(ColorBasic).
			MarginTop(1).
			Width(58)

	ErrorStyle = lipgloss.NewStyle().Foreground(ColorError)

	HelpStyle = lipgloss.NewStyle().Foreground(ColorMuted)
)
