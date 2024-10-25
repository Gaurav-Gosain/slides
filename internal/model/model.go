package model

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/golang/freetype"
	"github.com/maaslalani/slides/internal/file"
	"github.com/maaslalani/slides/internal/navigation"
	"github.com/maaslalani/slides/internal/process"
	"github.com/maaslalani/slides/internal/slides"
	"github.com/maaslalani/slides/internal/term"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/maaslalani/slides/internal/code"
	"github.com/maaslalani/slides/internal/meta"
	"github.com/maaslalani/slides/styles"
)

var (
	//go:embed tutorial.md
	slidesTutorial []byte
	tabSpaces      = strings.Repeat(" ", 4)
)

const (
	delimiter = "\n---\n"
)

const headerCells = 3

// Model represents the model of this presentation, which contains all the
// state related to the current slides.
type Model struct {
	Slides   []slides.Slide
	Page     int
	Author   string
	Date     string
	Theme    glamour.TermRendererOption
	Paging   string
	FileName string
	viewport viewport.Model
	buffer   string
	// VirtualText is used for additional information that is not part of the
	// original slides, it will be displayed on a slide and reset on page change
	VirtualText      string
	Search           navigation.Search
	TerminalProtocol term.TerminalProtocol
}

type fileWatchMsg struct{}

var fileInfo os.FileInfo

// Init initializes the model and begins watching the slides file for changes
// if it exists.
func (m Model) Init() tea.Cmd {
	if m.FileName == "" {
		return nil
	}
	fileInfo, _ = os.Stat(m.FileName)
	// return fileWatchCmd()
	return nil
}

func fileWatchCmd() tea.Cmd {
	return tea.Every(time.Second, func(t time.Time) tea.Msg {
		return fileWatchMsg{}
	})
}

// Load loads all of the content and metadata for the presentation.
func (m *Model) Load() error {
	var content string
	var err error

	if m.FileName != "" {
		content, err = readFile(m.FileName)
	} else {
		content, err = readStdin()
	}

	if err != nil {
		return err
	}

	content = strings.ReplaceAll(content, "\r", "")

	content = strings.TrimPrefix(content, strings.TrimPrefix(delimiter, "\n"))
	slides := strings.Split(content, delimiter)

	metaData, exists := meta.New().Parse(slides[0])
	// If the user specifies a custom configuration options
	// skip the first "slide" since this is all configuration
	if exists && len(slides) > 1 {
		slides = slides[1:]
	}

	m.Slides = m.parseSlides(slides)
	m.Author = metaData.Author
	m.Date = metaData.Date
	m.Paging = metaData.Paging
	if m.Theme == nil {
		m.Theme = styles.SelectTheme(metaData.Theme)
	}

	return nil
}

func (m *Model) updateSlides() {
	for i, slide := range m.Slides {
		header := slide.Header
		img := slide.Image
		headerStr := ""
		if header != nil {
			headerStr = code.RenderImage(header, m.TerminalProtocol, headerCells, m.viewport.Width)
		}
		imageStr := ""
		if img != nil {
			imageStr = code.RenderImage(img, m.TerminalProtocol, m.viewport.Height, m.viewport.Width)
		}
		m.Slides[i].HeaderStr = headerStr
		m.Slides[i].ImageStr = imageStr
	}
}

func (m *Model) parseSlides(slidesStr []string) []slides.Slide {
	newSlides := make([]slides.Slide, len(slidesStr))
	for i, slide := range slidesStr {
		header, slide := preprocessHeader(slide)
		img, slide := preprocessImage(slide)
		newSlides[i] = slides.Slide{
			Content: slide,
			Header:  header,
			Image:   img,
		}
	}

	return newSlides
}

func (m *Model) ExecuteCode() {
	// Run code blocks
	blocks, err := code.Parse(m.Slides[m.Page].Content)
	if err != nil {
		// We couldn't parse the code block on the screen
		m.VirtualText = "\n" + err.Error()
		return
	}
	var outs []string

	for _, block := range blocks {
		res := code.Execute(
			block,
			m.TerminalProtocol,
			m.GetAvailableCells(),
			m.viewport.Width,
		)
		outs = append(outs, res.Out)
	}
	m.VirtualText = strings.TrimSpace(strings.Join(outs, "\n"))
}

type autoExecuteCodeMsg struct{}

// Update updates the presentation model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height
		m.VirtualText = ""
		m.updateSlides()
		return m, ClearScreen

	case autoExecuteCodeMsg:
		m.AutoExecuteCode()
		return m, nil

	case tea.KeyMsg:
		keyPress := msg.String()

		if m.Search.Active {
			switch msg.Type {
			case tea.KeyEnter:
				// execute current buffer
				if m.Search.Query() != "" {
					m.Search.Execute(&m)
				} else {
					m.Search.Done()
				}
				// cancel search
				return m, nil
			case tea.KeyCtrlC, tea.KeyEscape:
				// quit command mode
				m.Search.SetQuery("")
				m.Search.Done()
				return m, nil
			}

			var cmd tea.Cmd
			m.Search.SearchTextInput, cmd = m.Search.SearchTextInput.Update(msg)
			return m, cmd
		}

		switch keyPress {
		case "/":
			// Begin search
			m.Search.Begin()
			m.Search.SearchTextInput.Focus()
			return m, nil
		case "ctrl+n":
			// Go to next occurrence
			m.Search.Execute(&m)
		case "ctrl+x":
			m.VirtualText = ""
			return m, ClearScreen
		case "ctrl+e":
			m.ExecuteCode()
			return m, nil
		case "y":
			blocks, err := code.Parse(m.Slides[m.Page].Content)
			if err != nil {
				return m, nil
			}
			for _, b := range blocks {
				_ = clipboard.WriteAll(b.Code)
			}
			return m, nil
		case "ctrl+c", "q":
			return m, tea.Quit
		default:
			newState := navigation.Navigate(navigation.State{
				Buffer:      m.buffer,
				Page:        m.Page,
				TotalSlides: len(m.Slides),
			}, keyPress)
			m.buffer = newState.Buffer
			return m, m.SetPage(newState.Page)
		}

	case fileWatchMsg:
		newFileInfo, err := os.Stat(m.FileName)
		if err == nil && newFileInfo.ModTime() != fileInfo.ModTime() {
			fileInfo = newFileInfo
			_ = m.Load()
			if m.Page >= len(m.Slides) {
				m.Page = len(m.Slides) - 1
			}
		}
		return m, fileWatchCmd()
	}
	return m, nil
}

func (m Model) GetAvailableCells() int {
	slide, _ := m.GetSlide()
	return m.viewport.Height - lipgloss.Height(slide)
}

func (m Model) GetSlide() (string, bool) {
	currSlide := m.Slides[m.Page]

	if currSlide.Image != nil {
		return currSlide.ImageStr, true
	}

	r, _ := glamour.NewTermRenderer(m.Theme, glamour.WithWordWrap(m.viewport.Width))
	slide := currSlide.Content
	slide = code.HideComments(slide)
	header := ""
	if currSlide.Header != nil {
		header = currSlide.HeaderStr
	}
	slide, err := r.Render(slide)
	slide = strings.ReplaceAll(slide, "\t", tabSpaces)
	slide += m.VirtualText
	if err != nil {
		slide = fmt.Sprintf("Error: Could not render markdown! (%v)", err)
	}
	return header + styles.Slide.Render(slide), header != ""
}

func (m Model) GetStatusLine() string {
	var left string
	if m.Search.Active {
		// render search bar
		left = m.Search.SearchTextInput.View()
	} else {
		// render author and date
		left = styles.Author.Render(m.Author) + styles.Date.Render(m.Date)
	}

	right := styles.Page.Render(m.paging())
	return styles.Status.Render(styles.JoinHorizontal(left, right, m.viewport.Width))
}

// View renders the current slide in the presentation and the status bar which
// contains the author, date, and pagination information.
func (m Model) View() string {
	slide, _ := m.GetSlide()
	// offset := 0
	// if hasHeader {
	// 	offset = 3
	// }
	// return styles.JoinVertical(
	// 	slide,
	// 	m.GetStatusLine(),
	// 	m.viewport.Height-2,
	// )
	return slide
}

func (m *Model) paging() string {
	switch strings.Count(m.Paging, "%d") {
	case 2:
		return fmt.Sprintf(m.Paging, m.Page+1, len(m.Slides))
	case 1:
		return fmt.Sprintf(m.Paging, m.Page+1)
	default:
		return m.Paging
	}
}

func preprocessImage(content string) (image.Image, string) {
	re := regexp.MustCompile(`!\[(.*?)\]\((.*?)\)`)
	// return re.ReplaceAllStringFunc(content, func(match string) string {
	// 	return fmt.Sprintf("```img\n///%s\n```", strings.Split(match[:len(match)-1], "(")[1])
	// })

	// check if there is even an image
	if !re.MatchString(content) {
		return nil, content
	}

	// find the path to the image
	path := strings.Split(content, "(")[1]
	path = strings.Split(path, ")")[0]

	imgFile, err := os.Open(path)
	if err != nil {
		return nil, content
	}
	defer imgFile.Close()

	img, _, err := image.Decode(imgFile)
	if err != nil {
		return nil, content
	}

	// remove the image from the content
	content = re.ReplaceAllString(content, "")

	return img, content
}

// CreateImageFromText takes a string and generates an image.Image of that text using a TrueType font
func CreateImageFromText(text string, fontSize int) (image.Image, error) {
	// Load the font
	fontBytes, err := os.ReadFile("./assets/FiraMono-Regular.ttf") // Change this to the path to your TTF font file
	if err != nil {
		return nil, err
	}

	// Parse the font
	f, err := freetype.ParseFont(fontBytes)
	if err != nil {
		return nil, err
	}

	// Create a new RGBA image
	img := image.NewRGBA(image.Rect(0, 0, 2*fontSize*len(text)/3, 3*fontSize/2))

	draw.Draw(img, img.Bounds(), &image.Uniform{image.Transparent}, image.Point{}, draw.Src)

	// Set up the context for drawing
	c := freetype.NewContext()
	c.SetDPI(float64(fontSize))
	c.SetFont(f)
	c.SetFontSize(float64(fontSize))
	c.SetClip(img.Bounds())
	c.SetDst(img)
	c.SetSrc(image.NewUniform(color.RGBA{R: 255, G: 170, B: 0, A: 255}))

	// Draw the text on the image
	pt := freetype.Pt(fontSize/2, fontSize) // Starting point for the text
	_, err = c.DrawString(text, pt)
	if err != nil {
		return nil, err
	}

	return img, nil
}

func getFirstHeader(content string) string {
	// split the content into lines
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			return line
		}
	}
	return ""
}

func preprocessHeader(content string) (image.Image, string) {
	// get the first header
	match := getFirstHeader(content)

	if match == "" {
		return nil, content
	}

	img, err := CreateImageFromText(strings.TrimSpace(match[2:]), 72)
	if err != nil {
		return nil, content
	}

	return img, strings.Replace(content, match, "", 1)
}

func (m *Model) preprocessHeaders(content string) (string, string) {
	// get the first header
	match := getFirstHeader(content)

	if match == "" {
		return "", content
	}

	img, err := CreateImageFromText(strings.TrimSpace(match[2:]), 72)
	if err != nil {
		return "", content
	}

	// remove the header
	content = strings.Replace(content, match, "", 1)

	imgStr := code.RenderImage(img, m.TerminalProtocol, 4, m.viewport.Width)

	return imgStr, content
}

func readFile(path string) (string, error) {
	s, err := os.Stat(path)
	if err != nil {
		return "", errors.New("could not read file")
	}
	if s.IsDir() {
		return "", errors.New("can not read directory")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(b)

	// content = preprocessImages(content)

	// Pre-process slides if the file is executable to avoid
	// unintentional code execution when presenting slides
	if file.IsExecutable(s) {
		// Remove shebang if file has one
		if strings.HasPrefix(content, "#!") {
			content = strings.Join(strings.SplitN(content, "\n", 2)[1:], "\n")
		}

		content = process.Pre(content)
	}

	return content, err
}

func readStdin() (string, error) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}

	if stat.Mode()&os.ModeNamedPipe == 0 && stat.Size() == 0 {
		return string(slidesTutorial), nil
	}

	reader := bufio.NewReader(os.Stdin)
	var b strings.Builder

	for {
		r, _, err := reader.ReadRune()
		if err != nil && err == io.EOF {
			break
		}
		_, err = b.WriteRune(r)
		if err != nil {
			return "", err
		}
	}

	return b.String(), nil
}

// CurrentPage returns the current page the presentation is on.
func (m *Model) CurrentPage() int {
	return m.Page
}

type clearScreenMsg struct{}

var ClearScreen = tea.Sequence(tea.ClearScreen, func() tea.Msg {
	return autoExecuteCodeMsg{}
})

// func ClearScreenCmd() tea.Msg {
// 	return tea.ClearScreen
// }

func isAutoExecuteLanguage(language string) bool {
	for _, l := range []string{"qr", "img"} {
		if l == language {
			return true
		}
	}
	return false
}

// SetPage sets which page the presentation should render.
func (m *Model) SetPage(page int) tea.Cmd {
	if m.Page == page {
		return nil
	}

	m.VirtualText = ""
	m.Page = page

	return ClearScreen
}

func (m *Model) AutoExecuteCode() {
	// Run code blocks
	blocks, err := code.Parse(m.Slides[m.Page].Content)
	if err != nil {
		// We couldn't parse the code block on the screen
		m.VirtualText = ""
		return
	}
	var outs []string
	for _, block := range blocks {
		if isAutoExecuteLanguage(block.Language) {
			res := code.Execute(
				block,
				m.TerminalProtocol,
				m.GetAvailableCells(),
				m.viewport.Width,
			)
			outs = append(outs, res.Out)
		}
	}
	m.VirtualText = strings.TrimSpace(strings.Join(outs, "\n"))
}

// Pages returns all the slides in the presentation.
func (m *Model) Pages() []slides.Slide {
	return m.Slides
}
