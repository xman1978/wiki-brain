package pptx

// Deck is a semantic representation of a PPTX presentation.
type Deck struct {
	Title  string
	Slides []Slide
}

// Slide holds the extracted, ordered content blocks of one slide.
type Slide struct {
	Number int
	Title  string
	Blocks []Block
	Notes  string
}

// Block is one content unit. Type is one of: "paragraph", "bullet", "image", "table".
type Block struct {
	Type  string
	Text  string     // paragraph / bullet text
	Level int        // bullet indent, from the <a:pPr lvl> attribute
	Alt   string     // image alt (descr, else name)
	Rows  [][]string // table cells
}
