package main

import (
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// The palette is a dispatch desk at night: warm paper ink, an amber lamp over the entry I am on, ember for what is
// urgent, sea glass for what is handled. Each color has a counterpart for light terminals.
var (
	ink   = lipgloss.AdaptiveColor{Light: "#2B2620", Dark: "#E8E2D0"}
	dust  = lipgloss.AdaptiveColor{Light: "#8A8173", Dark: "#857E71"} // secondary text
	chalk = lipgloss.AdaptiveColor{Light: "#D6CFC2", Dark: "#3A362F"} // rules and borders
	lamp  = lipgloss.AdaptiveColor{Light: "#B86E00", Dark: "#F2A541"} // the cursor, the brand, keys
	ember = lipgloss.AdaptiveColor{Light: "#C2410C", Dark: "#EE6C4D"} // urgent, failed
	glass = lipgloss.AdaptiveColor{Light: "#2F7D6D", Dark: "#7CC6B4"} // approved, done
	slate = lipgloss.AdaptiveColor{Light: "#4A6A8A", Dark: "#8DB1D3"} // actions, links
	plum  = lipgloss.AdaptiveColor{Light: "#7A4E7E", Dark: "#C49BC8"} // projects, people
	shade = lipgloss.AdaptiveColor{Light: "#F1EBDD", Dark: "#2A2621"} // the cursor's row
)

var (
	plain        = lipgloss.NewStyle()
	styleInk     = lipgloss.NewStyle().Foreground(ink)
	styleTitle   = lipgloss.NewStyle().Foreground(ink).Bold(true)
	styleDust    = lipgloss.NewStyle().Foreground(dust)
	styleChalk   = lipgloss.NewStyle().Foreground(chalk)
	styleLamp    = lipgloss.NewStyle().Foreground(lamp).Bold(true)
	styleBrand   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#FFF8EC", Dark: "#1C1915"}).Background(lamp).Bold(true)
	styleUrgent  = lipgloss.NewStyle().Foreground(ember).Bold(true)
	styleFailed  = lipgloss.NewStyle().Foreground(ember)
	styleDone    = lipgloss.NewStyle().Foreground(glass)
	styleAction  = lipgloss.NewStyle().Foreground(slate)
	styleLink    = lipgloss.NewStyle().Foreground(slate).Underline(true)
	styleProject = lipgloss.NewStyle().Foreground(plum).Bold(true)
	styleActor   = lipgloss.NewStyle().Foreground(plum).Bold(true)
	stylePending = lipgloss.NewStyle().Foreground(lamp)
)

// blend is a color between two of the palette's, a fraction t of the way from one to the other.
func blend(from, to lipgloss.AdaptiveColor, t float64) lipgloss.Color {
	pick := func(c lipgloss.AdaptiveColor) colorful.Color {
		hex := c.Light
		if lipgloss.HasDarkBackground() {
			hex = c.Dark
		}
		col, _ := colorful.Hex(hex)
		return col
	}
	return lipgloss.Color(pick(from).BlendLab(pick(to), math.Max(0, math.Min(1, t))).Clamped().Hex())
}

// The view moves a little: the progress line fills as I decide, a verdict lands on its row with a bounce, and the
// status dot glows when something happens and then cools. Each is a spring, stepped once a frame until it rests.
const fps = 60

var (
	gaugeSpring = harmonica.NewSpring(harmonica.FPS(fps), 6, 1)    // critically damped: fills without overshooting
	stampSpring = harmonica.NewSpring(harmonica.FPS(fps), 14, 0.3) // under-damped: lands and bounces
	flashSpring = harmonica.NewSpring(harmonica.FPS(fps), 2.5, 1)
)

type motion struct {
	spring           harmonica.Spring
	pos, vel, target float64
}

func (s *motion) step() { s.pos, s.vel = s.spring.Update(s.pos, s.vel, s.target) }

func (s *motion) still() bool { return math.Abs(s.pos-s.target) < 0.002 && math.Abs(s.vel) < 0.002 }

func (s *motion) rest() { s.pos, s.vel = s.target, 0 }

// from starts the spring at pos, headed for target.
func (s *motion) from(pos, target float64) { s.pos, s.vel, s.target = pos, 0, target }

type frame struct{}

func nextFrame() tea.Cmd {
	return tea.Tick(time.Second/fps, func(time.Time) tea.Msg { return frame{} })
}

func (m *model) springs() []*motion { return []*motion{&m.gauge, &m.stamp, &m.flash} }

// animate sets the springs going toward where the model now stands. Without motion they jump there.
func (m *model) animate() tea.Cmd {
	m.gauge.target = m.progress()
	moving := false
	for _, s := range m.springs() {
		if !m.motion {
			s.rest()
		}
		moving = moving || !s.still()
	}
	if !moving || m.animating {
		return nil
	}
	m.animating = true
	return nextFrame()
}

func (m *model) stepFrame() tea.Cmd {
	moving := false
	for _, s := range m.springs() {
		if s.step(); s.still() {
			s.rest()
		} else {
			moving = true
		}
	}
	if !moving {
		m.animating = false
		return nil
	}
	return nextFrame()
}
