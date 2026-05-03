package main

import (
	"fmt"
	"os"
	"strings"
)

// ANSI escape codes for console output.
// These work on PXE initramfs consoles (linux/vt100/serial).
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

const consoleWidth = 72

// consolePrint writes a line to stderr (the operator's console).
func consolePrint(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
}

// consolePrintln writes a line + newline to stderr.
func consolePrintln(s string) {
	fmt.Fprintln(os.Stderr, s)
}

// printDeployHeader prints the branded deploy banner with node/image/server info.
// Fields left empty are omitted from the box.
func printDeployHeader(nodeName, imageName, serverURL string) {
	consolePrintln("")
	consolePrintln(ansiBold + ansiCyan + "╔" + strings.Repeat("═", consoleWidth-2) + "╗" + ansiReset)
	consolePrintln(ansiBold + ansiCyan + "║" + ansiReset + centreLabel("clustr — Node Deployment", consoleWidth-2) + ansiBold + ansiCyan + "║" + ansiReset)
	consolePrintln(ansiBold + ansiCyan + "║" + strings.Repeat(" ", consoleWidth-2) + "║" + ansiReset)
	if nodeName != "" {
		consolePrintln(ansiBold + ansiCyan + "║" + ansiReset + labelField("  Node", nodeName, consoleWidth-2) + ansiBold + ansiCyan + "║" + ansiReset)
	}
	if imageName != "" {
		consolePrintln(ansiBold + ansiCyan + "║" + ansiReset + labelField("  Image", imageName, consoleWidth-2) + ansiBold + ansiCyan + "║" + ansiReset)
	}
	if serverURL != "" {
		consolePrintln(ansiBold + ansiCyan + "║" + ansiReset + labelField("  Server", serverURL, consoleWidth-2) + ansiBold + ansiCyan + "║" + ansiReset)
	}
	consolePrintln(ansiBold + ansiCyan + "╚" + strings.Repeat("═", consoleWidth-2) + "╝" + ansiReset)
	consolePrintln("")
}

// centreLabel centres text within width spaces, padding with spaces on both sides.
func centreLabel(text string, width int) string {
	if len(text) >= width {
		return text[:width]
	}
	left := (width - len(text)) / 2
	right := width - len(text) - left
	return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
}

// labelField formats "  Key:    Value" padded to width characters.
func labelField(key, value string, width int) string {
	label := fmt.Sprintf("%s:    %s", key, value)
	if len(label) >= width {
		return label[:width]
	}
	return label + strings.Repeat(" ", width-len(label))
}

// phaseStatus constants for printPhase.
type phaseStatus int

const (
	phasePending    phaseStatus = iota // [ ]
	phaseInProgress                    // [▸]
	phaseDone                          // [✓]
	phaseFailed                        // [✗]
)

// printPhase prints a single phase status line.
// Leading newline=true adds a blank line before the first phase for spacing.
func printPhase(status phaseStatus, label string) {
	var icon, color string
	switch status {
	case phaseInProgress:
		icon = "▸"
		color = ansiYellow
	case phaseDone:
		icon = "✓"
		color = ansiGreen
	case phaseFailed:
		icon = "✗"
		color = ansiRed
	default:
		icon = " "
		color = ""
	}

	if color != "" {
		consolePrintln(fmt.Sprintf("  %s[%s]%s %s", color, icon, ansiReset, label))
	} else {
		consolePrintln(fmt.Sprintf("  [%s] %s", icon, label))
	}
}

// printPhaseUpdate overwrites the current line with an in-progress phase + detail.
// Call with \r — no trailing newline. Use consolePrintln("") to advance after done.
func printPhaseUpdate(label, detail string) {
	line := fmt.Sprintf("  %s[▸]%s %-28s  %s", ansiYellow, ansiReset, label, detail)
	fmt.Fprintf(os.Stderr, "\r%-*s", consoleWidth, line)
}

// printProgressBar renders an in-place progress line with a bar, bytes, and percent.
// Call repeatedly with \r semantics; call consolePrintln("") when done.
func printProgressBar(label string, written, total int64) {
	const barWidth = 24
	var bar, detail string

	if total > 0 {
		pct := float64(written) / float64(total)
		if pct > 1.0 {
			pct = 1.0
		}
		filled := int(pct * barWidth)
		if filled > barWidth {
			filled = barWidth
		}
		bar = strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
		detail = fmt.Sprintf("%s / %s  [%s]  %3.0f%%",
			humanBytes(written), humanBytes(total), bar, pct*100)
	} else {
		bar = strings.Repeat("░", barWidth)
		detail = fmt.Sprintf("%s  [%s]", humanBytes(written), bar)
	}

	line := fmt.Sprintf("  %s[▸]%s %-28s  %s", ansiYellow, ansiReset, label+"...", detail)
	fmt.Fprintf(os.Stderr, "\r%-*s", consoleWidth, line)
}

// printDeployError prints the failure box shown on a failed deploy.
func printDeployError(phase, errMsg string) {
	border := "┌─ " + ansiBold + ansiRed + "DEPLOY FAILED" + ansiReset + " " + strings.Repeat("─", consoleWidth-18) + "┐"
	consolePrintln("")
	consolePrintln(border)
	consolePrintln("│  " + padRight("Phase:  "+phase, consoleWidth-4) + "  │")
	// Wrap error message at consoleWidth-6 chars.
	wrapped := wrapText("Error:  "+errMsg, consoleWidth-6)
	for _, line := range wrapped {
		consolePrintln("│  " + padRight(line, consoleWidth-4) + "  │")
	}
	consolePrintln("│" + strings.Repeat(" ", consoleWidth-2) + "│")
	consolePrintln("│  " + padRight("The node will PXE boot and retry on next power cycle.", consoleWidth-4) + "  │")
	consolePrintln("└" + strings.Repeat("─", consoleWidth-2) + "┘")
	consolePrintln("")
}

// phaseLabel converts a deployer phase name (e.g. "downloading") to a
// human-readable label for display on the console.
func phaseLabel(phase string) string {
	switch phase {
	case "partitioning":
		return "Partitioning disk"
	case "formatting":
		return "Formatting partitions"
	case "downloading":
		return "Downloading image"
	case "extracting":
		return "Extracting filesystem"
	case "finalizing":
		return "Finalizing"
	case "preflight":
		return "Preflight checks"
	case "deploy-complete":
		return "Deploy-complete"
	default:
		if phase == "" {
			return "Working"
		}
		return phase
	}
}

// padRight pads s with spaces to exactly length n. Truncates if longer.
func padRight(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

// wrapText splits text into lines no longer than maxWidth.
func wrapText(text string, maxWidth int) []string {
	if len(text) <= maxWidth {
		return []string{text}
	}
	var lines []string
	for len(text) > maxWidth {
		lines = append(lines, text[:maxWidth])
		text = text[maxWidth:]
	}
	if text != "" {
		lines = append(lines, text)
	}
	return lines
}
