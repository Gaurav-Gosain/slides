package code

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "image/jpeg"
	_ "image/png"

	"github.com/charmbracelet/lipgloss"
	"github.com/maaslalani/slides/internal/term"
	"github.com/mdp/qrterminal/v3"
)

// Block represents a code block.
type Block struct {
	Code     string
	Language string
}

// Result represents the output for an executed code block.
type Result struct {
	Out           string
	ExitCode      int
	ExecutionTime time.Duration
}

// ?: means non-capture group
var re = regexp.MustCompile("(?s)(?:```|~~~)(\\w+)\n(.*?)\n(?:```|~~~)\\s?")

// ErrParse is the returned error when we cannot parse the code block (i.e.
// there is no code block on the current slide) or the code block is
// incorrectly written.
var ErrParse = errors.New("Error: could not parse code block")

// Parse takes a block of markdown and returns an array of Block's with code
// and associated languages
func Parse(markdown string) ([]Block, error) {
	matches := re.FindAllStringSubmatch(markdown, -1)

	var rv []Block
	for _, match := range matches {
		// There was either no language specified or no code block
		// Either way, we cannot execute the expression
		if len(match) < 3 {
			continue
		}
		rv = append(rv, Block{
			Language: match[1],
			Code:     RemoveComments(match[2]),
		})

	}

	if len(rv) == 0 {
		return nil, ErrParse
	}

	return rv, nil
}

const (
	// ExitCodeInternalError represents the exit code in which the code
	// executing the code didn't work.
	ExitCodeInternalError = -1
)

func RenderImage(img image.Image, terminal term.TerminalProtocol, availableCells int, width int) string {
	var buff bytes.Buffer

	aspectRatio := 2.2 * float64(img.Bounds().Dx()) / float64(img.Bounds().Dy())

	rows := float64(availableCells)
	cols := float64(rows) * aspectRatio

	if cols > float64(width) {
		cols = float64(width)
		// recalculate rows
		rows = cols / aspectRatio
	}

	switch terminal {
	case term.Kitty:
		// Kitty options
		kittyImgOpts := term.KittyImgOpts{
			DstRows: uint32(rows),
			DstCols: uint32(cols),
		}
		term.KittyWriteImage(&buff, img, kittyImgOpts)
	case term.Iterm:
		// iTerm options
		itermOpts := term.ItermImgOpts{
			Height: fmt.Sprint(rows),
			Width:  fmt.Sprint(cols),
		}
		term.ItermWriteImageWithOptions(&buff, img, itermOpts)
	default:
		// 	ansi art
	}
	return buff.String()
}

// Execute takes a code.Block and returns the output of the executed code
func Execute(code Block, terminal term.TerminalProtocol, availableCells int, width int) Result {
	if code.Language == "img" {
		f, err := os.Open(code.Code)
		if err != nil {
			panic(err)
		}
		defer f.Close()

		img, _, err := image.Decode(f)
		if err != nil {
			panic(err)
		}

		return Result{
			Out:      RenderImage(img, terminal, availableCells, width),
			ExitCode: 0,
		}
	}

	if code.Language == "qr" {
		qrCodesURLs := strings.Split(code.Code, "\n")

		qrCodes := make([]string, len(qrCodesURLs))

		for i, qrCode := range qrCodesURLs {
			var buff bytes.Buffer

			config := qrterminal.Config{
				Level:          qrterminal.L,
				Writer:         &buff,
				HalfBlocks:     true,
				BlackChar:      qrterminal.BLACK_BLACK,
				WhiteBlackChar: qrterminal.WHITE_BLACK,
				WhiteChar:      qrterminal.WHITE_WHITE,
				BlackWhiteChar: qrterminal.BLACK_WHITE,
				QuietZone:      1,
			}

			qrterminal.GenerateWithConfig(
				strings.TrimSpace(qrCode),
				config,
			)

			qrCodes[i] = lipgloss.
				NewStyle().
				PaddingRight(8).
				Render(
					lipgloss.JoinVertical(
						lipgloss.Left,
						buff.String(),
						lipgloss.NewStyle().Foreground(lipgloss.Color("#4169E1")).Render(qrCode),
					),
				)
		}

		qrCodeString := lipgloss.JoinHorizontal(lipgloss.Left, qrCodes...)

		return Result{
			Out:      qrCodeString,
			ExitCode: 0,
		}
	}

	// Check supported language
	language, ok := Languages[code.Language]
	if !ok {
		return Result{
			Out:      "Error: unsupported language",
			ExitCode: ExitCodeInternalError,
		}
	}

	// Write the code block to a temporary file
	codeDir := os.TempDir()
	f, err := os.CreateTemp(codeDir, "slides-*."+Languages[code.Language].Extension)
	if err != nil {
		return Result{
			Out:      "Error: could not create file",
			ExitCode: ExitCodeInternalError,
		}
	}

	defer f.Close()
	defer os.Remove(f.Name())

	_, err = f.WriteString(code.Code)
	if err != nil {
		return Result{
			Out:      "Error: could not write to file",
			ExitCode: ExitCodeInternalError,
		}
	}

	var (
		output   strings.Builder
		exitCode int
	)

	// replacer for commands
	repl := strings.NewReplacer(
		"<file>", f.Name(),
		// <name>: file name without extension and without path
		"<name>", filepath.Base(strings.TrimSuffix(f.Name(), filepath.Ext(f.Name()))),
		"<path>", filepath.Dir(f.Name()),
	)

	// For accuracy of program execution speed, we can't put anything after
	// recording the start time or before recording the end time.
	start := time.Now()

	for _, c := range language.Commands {

		var command []string
		// replace <file>, <name> and <path> in commands
		for _, v := range c {
			command = append(command, repl.Replace(v))
		}
		// execute and write output
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Dir = codeDir
		out, err := cmd.Output()
		if err != nil {
			output.Write([]byte(err.Error()))
		} else {
			output.Write(out)
		}

		// update status code
		if err != nil {
			if cmd.ProcessState != nil {
				exitCode = cmd.ProcessState.ExitCode()
			} else {
				exitCode = 1 // non-zero
			}
		}
	}

	end := time.Now()

	return Result{
		Out:           output.String(),
		ExitCode:      exitCode,
		ExecutionTime: end.Sub(start),
	}
}
