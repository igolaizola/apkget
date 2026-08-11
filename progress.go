package apkget

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const progressBarWidth = 28

type progressBar struct {
	name      string
	total     int64
	current   int64
	started   time.Time
	lastDraw  time.Time
	completed bool
	mu        sync.Mutex
}

func newProgressBar(name string, total int64, enabled bool) *progressBar {
	if !enabled {
		return nil
	}
	return &progressBar{name: name, total: total}
}

func (p *progressBar) start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = time.Now()
	p.drawLocked(false)
}

func (p *progressBar) Write(data []byte) (int, error) {
	// io.Copy may write large chunks, so redraw at most ten times per second
	// instead of once for every chunk.
	p.mu.Lock()
	p.current += int64(len(data))
	if time.Since(p.lastDraw) >= 100*time.Millisecond || (p.total > 0 && p.current >= p.total) {
		p.drawLocked(false)
	}
	p.mu.Unlock()
	return len(data), nil
}

func (p *progressBar) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completed {
		return
	}
	p.completed = true
	p.drawLocked(true)
	fmt.Fprintln(os.Stderr)
}

func (p *progressBar) drawLocked(final bool) {
	if p.started.IsZero() {
		p.started = time.Now()
	}
	p.lastDraw = time.Now()
	duration := time.Since(p.started)
	if duration <= 0 {
		duration = time.Nanosecond
	}
	rate := float64(p.current) / duration.Seconds()
	speed := formatBytes(int64(rate)) + "/s"

	var status string
	if p.total > 0 {
		// A known Content-Length gives a determinate bar and an ETA.
		percent := float64(p.current) / float64(p.total)
		if percent > 1 {
			percent = 1
		}
		filled := int(percent * progressBarWidth)
		bar := strings.Repeat("█", filled) + strings.Repeat("░", progressBarWidth-filled)
		status = fmt.Sprintf("[%s] %3.0f%%  %s / %s  %s", bar, percent*100, formatBytes(p.current), formatBytes(p.total), speed)
		if !final && p.current < p.total {
			remaining := time.Duration(float64(p.total-p.current) / rate * float64(time.Second))
			status += "  " + formatDuration(remaining)
		}
	} else {
		// Chunked responses cannot provide a percentage, so show a spinner.
		status = fmt.Sprintf("%s  %s  %s", spinner(int(duration/(120*time.Millisecond))), formatBytes(p.current), speed)
	}
	fmt.Fprintf(os.Stderr, "\r%-72s  %s", status, p.name)
}

func spinner(step int) string {
	return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[step%10]
}

func formatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	amount := float64(value)
	for _, unit := range units {
		amount /= 1024
		if amount < 1024 || unit == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", amount, unit)
		}
	}
	return fmt.Sprintf("%d B", value)
}

func formatDuration(value time.Duration) string {
	if value < time.Second {
		return "ETA <1s"
	}
	if value < time.Minute {
		return fmt.Sprintf("ETA %ds", int(value.Round(time.Second).Seconds()))
	}
	return fmt.Sprintf("ETA %dm%02ds", int(value/time.Minute), int(value/time.Second)%60)
}

func isTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
