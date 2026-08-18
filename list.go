package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"
)

const (
	formatPlain       = "plain"
	formatMDChecklist = "md-checklist"
	formatCSV         = "csv"
	formatTSV         = "tsv"

	deckTitle = "# Steam Backlog"
)

var validFormats = []string{formatPlain, formatMDChecklist, formatCSV, formatTSV}

// "unplayed" with no threshold, "under 2.5h" otherwise
func poolLabel(thresholdHours float64) string {
	if thresholdHours <= 0 {
		return "unplayed"
	}
	return "under " + hoursText(thresholdHours)
}

// trailing zeros trimmed: "2h", "2.5h"
func hoursText(hours float64) string {
	return strconv.FormatFloat(hours, 'f', -1, 64) + "h"
}

func listGames(games []game, skips map[int]bool, opt options) error {
	p := pool(games, skips, opt.thresholdMins)
	if opt.drawdeck != "" {
		return writeDeck(opt.drawdeck, p, opt.thresholdHours, excludedSkips(games, skips, opt.thresholdMins))
	}
	return printList(p, opt.format)
}

func printList(games []game, format string) error {
	switch format {
	case formatCSV, formatTSV:
		w := csv.NewWriter(os.Stdout)
		if format == formatTSV {
			w.Comma = '\t'
		}
		for _, g := range games {
			hours := fmt.Sprintf("%.1f", float64(g.Playtime)/60)
			if err := w.Write([]string{strconv.Itoa(g.AppID), g.Name, hours}); err != nil {
				return err
			}
		}
		w.Flush()
		return w.Error()
	case formatMDChecklist:
		for _, g := range games {
			fmt.Println("- [ ] " + g.Name)
		}
	default:
		for _, g := range games {
			fmt.Println(g.Name)
		}
	}
	return nil
}

// owned games that would have qualified but are skipped
func excludedSkips(games []game, skips map[int]bool, thresholdMins int) int {
	n := 0
	for _, g := range games {
		if skips[g.AppID] && qualifies(g, thresholdMins) {
			n++
		}
	}
	return n
}

type deckItem struct {
	name    string
	checked bool
}

var checkedRe = regexp.MustCompile(`^\s*- \[[xX]\]\s+(.*\S)\s*$`)

// titles already checked off in an existing deck, so a refresh keeps progress
func checkedTitles(path string) (map[string]bool, error) {
	checked := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return checked, nil
		}
		return nil, err
	}
	for line := range strings.Lines(string(data)) {
		if m := checkedRe.FindStringSubmatch(line); m != nil {
			checked[m[1]] = true
		}
	}
	return checked, nil
}

// rewrites path as a drawdeck, carrying every checked title through even when
// it no longer qualifies
func writeDeck(path string, games []game, thresholdHours float64, excluded int) error {
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return err
	}

	exists := true
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		exists = false
	}
	if exists && !confirmOverwrite(path) {
		fmt.Println(HelpStyle.Render("left " + path + " alone"))
		return nil
	}

	checked, err := checkedTitles(path)
	if err != nil {
		return err
	}

	items := make([]deckItem, 0, len(games)+len(checked))
	inPool := map[string]bool{}
	for _, g := range games {
		inPool[g.Name] = true
		items = append(items, deckItem{name: g.Name, checked: checked[g.Name]})
	}
	for name := range checked {
		if !inPool[name] {
			items = append(items, deckItem{name: name, checked: true})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := sortKey(items[i].name), sortKey(items[j].name)
		if a != b {
			return a < b
		}
		return items[i].name < items[j].name
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n\n", deckTitle, thresholdLine(thresholdHours, excluded))
	for _, it := range items {
		box := "- [ ] "
		if it.checked {
			box = "- [x] "
		}
		b.WriteString(box + it.name + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}

	fmt.Printf("wrote %d games to %s\n", len(items), path)
	return nil
}

func thresholdLine(hours float64, excluded int) string {
	s := "Threshold: unplayed"
	if hours > 0 {
		s = "Threshold: " + hoursText(hours)
	}
	switch {
	case excluded == 1:
		s += ". 1 game was marked as skipped and was excluded."
	case excluded > 1:
		s += fmt.Sprintf(". %d games were marked as skipped and were excluded.", excluded)
	}
	return s
}

// piped runs overwrite without asking
func confirmOverwrite(path string) bool {
	if !term.IsTerminal(os.Stdin.Fd()) {
		return true
	}
	fmt.Print(HelpStyle.Render(path + " exists — refreshing keeps your checked items. overwrite? [Y/n] "))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	return !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "n")
}
