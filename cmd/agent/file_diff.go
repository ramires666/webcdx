package main

import (
	"fmt"
	"log"
	"slices"
	"strings"
)

type diffLine struct {
	kind   byte
	text   string
	number int
}

// Myers finds changed lines without a quadratic table for a long, lightly edited file.
func changedLines(before, after string) ([]diffLine, bool) {
	lines := func(text string) []string {
		if text == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
	}
	a, b := lines(before), lines(after)
	// shortcut: bound preview work to 20,000 lines and 256 edits, use an IDE for larger rewrites.
	if len(a)+len(b) > 20000 {
		return nil, false
	}
	const limit = 256
	const offset = limit + 1
	v := make([]int, 2*limit+3)
	trace := [][]int{}
	for d := 0; d <= limit; d++ {
		for k := -d; k <= d; k += 2 {
			x := v[offset+k-1] + 1
			if k == -d || k != d && v[offset+k-1] < v[offset+k+1] {
				x = v[offset+k+1]
			}
			y := x - k
			for x < len(a) && y < len(b) && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x < len(a) || y < len(b) {
				continue
			}
			result := []diffLine{}
			x, y = len(a), len(b)
			for step := d; step > 0; step-- {
				previous := trace[step-1]
				k := x - y
				prevK := k - 1
				if k == -step || k != step && previous[offset+k-1] < previous[offset+k+1] {
					prevK = k + 1
				}
				prevX, prevY := previous[offset+prevK], previous[offset+prevK]-prevK
				for x > prevX && y > prevY {
					result = append(result, diffLine{kind: ' '})
					x--
					y--
				}
				if x == prevX {
					result = append(result, diffLine{'+', b[y-1], y})
					y--
				} else {
					result = append(result, diffLine{'-', a[x-1], x})
					x--
				}
			}
			slices.Reverse(result)
			return result, true
		}
		trace = append(trace, slices.Clone(v))
	}
	return nil, false
}

func formatFileDiff(before, after string) string {
	if before == after {
		return "Без изменений"
	}
	changes, ok := changedLines(before, after)
	if !ok {
		return "Изменения слишком большие для краткого просмотра; откройте сравнение в редакторе."
	}
	const width = 48
	column := func(line diffLine) string {
		text := []rune(consoleText(fmt.Sprintf("%d %s", line.number, line.text)))
		if len(text) > width-2 {
			return string(text[:width-5]) + "..."
		}
		return string(text)
	}
	var out strings.Builder
	beforeTitle, afterTitle := "До", "После"
	if before == "" {
		beforeTitle += " (пусто)"
	}
	if after == "" {
		afterTitle += " (пусто)"
	}
	fmt.Fprintf(&out, "%-*s │ %s\n", width, beforeTitle, afterTitle)
	rows := 0
	for i := 0; i < len(changes); {
		if changes[i].kind == ' ' {
			i++
			continue
		}
		removed, added := []diffLine{}, []diffLine{}
		for i < len(changes) && changes[i].kind != ' ' {
			if changes[i].kind == '-' {
				removed = append(removed, changes[i])
			} else {
				added = append(added, changes[i])
			}
			i++
		}
		for row := 0; row < max(len(removed), len(added)); row++ {
			if rows == 40 {
				out.WriteString("… остальные изменения скрыты\n")
				return out.String()
			}
			left, right := "", ""
			if row < len(removed) {
				left = "- " + column(removed[row])
			} else if row == len(removed) {
				left = "(нет строки)"
			}
			if row < len(added) {
				right = "+ " + column(added[row])
			} else if row == len(added) {
				right = "(нет строки)"
			}
			fmt.Fprintf(&out, "%-*s │ %s\n", width, left, right)
			rows++
		}
	}
	if rows == 0 {
		return "Изменились только переводы строк или завершающий перенос строки"
	}
	return out.String()
}

func logFileChange(path string, before, after []byte, created bool) {
	action := "Сохранён файл"
	if created {
		action = "Создан файл (ранее не существовал)"
	}
	preview := formatFileDiff(string(before), string(after))
	if created && len(after) == 0 {
		preview = "Пустой файл"
	}
	log.Printf("✓ %s %s\n%s", action, compactString(path, 180), preview)
}
