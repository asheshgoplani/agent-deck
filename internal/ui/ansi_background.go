package ui

import (
	"math"
	"strconv"
	"strings"
)

// darkBackgroundLuminance is the WCAG relative luminance below which a
// captured SGR background counts as dark. In light theme only dark
// backgrounds are remapped onto the preview surface (#322/#334); light ones
// are the tool's own rendering for a light terminal and pass through (#2449).
const darkBackgroundLuminance = 0.5

// basicBackgroundIsDark classifies the 16 terminal palette slots, whose
// actual RGB is the user's terminal scheme and unknown here, by their
// conventional role: the dim colours 0-6 and bright black 8 (a dark grey,
// the darkest slot in schemes such as Solarized) are dark; white 7 and the
// bright variants 9-15 are light.
var basicBackgroundIsDark = [16]bool{
	true, true, true, true, true, true, true, false,
	true, false, false, false, false, false, false, false,
}

// isDarkANSIBackground reports whether seq, one match of ansiBackgroundRE,
// sets a dark background. A sequence it cannot parse counts as dark, so it
// keeps the pre-#2449 behaviour of being remapped.
func isDarkANSIBackground(seq string) bool {
	params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
	code, err := strconv.Atoi(params[0])
	if err != nil {
		return true
	}
	switch {
	case code >= 40 && code <= 47:
		return basicBackgroundIsDark[code-40]
	case code >= 100 && code <= 107:
		return basicBackgroundIsDark[code-100+8]
	case code != 48 || len(params) < 3:
		return true
	}
	switch params[1] {
	case "5":
		n, err := strconv.Atoi(params[2])
		if err != nil || n < 0 || n > 255 {
			return true
		}
		if n < 16 {
			return basicBackgroundIsDark[n]
		}
		r, g, b := xterm256RGB(n)
		return relativeLuminance(r, g, b) < darkBackgroundLuminance
	case "2":
		if len(params) < 5 {
			return true
		}
		var rgb [3]int
		for i := range rgb {
			v, err := strconv.Atoi(params[2+i])
			if err != nil || v < 0 || v > 255 {
				return true
			}
			rgb[i] = v
		}
		return relativeLuminance(rgb[0], rgb[1], rgb[2]) < darkBackgroundLuminance
	}
	return true
}

// xterm256RGB returns the xterm default RGB of a 256-colour index >= 16:
// the 6x6x6 colour cube (16-231) or the greyscale ramp (232-255).
func xterm256RGB(n int) (r, g, b int) {
	if n >= 232 {
		v := 8 + 10*(n-232)
		return v, v, v
	}
	levels := [6]int{0, 95, 135, 175, 215, 255}
	n -= 16
	return levels[n/36], levels[(n/6)%6], levels[n%6]
}

// relativeLuminance is the WCAG 2 relative luminance of an sRGB colour, in [0, 1].
func relativeLuminance(r, g, b int) float64 {
	linear := func(c int) float64 {
		v := float64(c) / 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}
