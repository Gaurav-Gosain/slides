package slides

import "image"

type Slide struct {
	Header    image.Image
	Image     image.Image
	Content   string
	HeaderStr string
	ImageStr  string
}
