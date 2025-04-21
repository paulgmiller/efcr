package main

// ecfra
import "encoding/xml"

// ---------------------------------------------------------------------------
// Top‑level wrappers
// ---------------------------------------------------------------------------

// <DLPSTEXTCLASS> … <HEADER> … <TEXT> …
type ECFRFile struct {
	XMLName xml.Name `xml:"DLPSTEXTCLASS"`
	Header  Header   `xml:"HEADER"`
	Text    Text     `xml:"TEXT"`
}

// ---------------------------------------------------------------------------
// <HEADER> block  (rarely used, but modelled for completeness)
// ---------------------------------------------------------------------------

type Header struct {
	FileDesc    FileDesc    `xml:"FILEDESC"`
	ProfileDesc ProfileDesc `xml:"PROFILEDESC"`
}

type FileDesc struct {
	TitleStmt       TitleStmt       `xml:"TITLESTMT"`
	PublicationStmt PublicationStmt `xml:"PUBLICATIONSTMT"`
	SeriesStmt      SeriesStmt      `xml:"SERIESSTMT"`
}

type TitleStmt struct {
	Title  string `xml:"TITLE"`
	Author Author `xml:"AUTHOR"`
}

type Author struct {
	Type  string `xml:"TYPE,attr,omitempty"`
	Value string `xml:",chardata"`
}

type PublicationStmt struct {
	Publisher string `xml:"PUBLISHER"`
	PubPlace  string `xml:"PUBPLACE"`
	IDNo      IDNo   `xml:"IDNO"`
	Date      string `xml:"DATE"`
}

type IDNo struct {
	Type  string `xml:"TYPE,attr,omitempty"`
	Value string `xml:",chardata"`
}

type SeriesStmt struct {
	Title string `xml:"TITLE"`
}

type ProfileDesc struct {
	TextClass TextClass `xml:"TEXTCLASS"`
}

type TextClass struct {
	Keywords string `xml:"KEYWORDS"`
}

// ---------------------------------------------------------------------------
// Body = <TEXT><BODY><ECFRBRWS> …
// ---------------------------------------------------------------------------

type Text struct {
	Body Body `xml:"BODY"`
}

type Body struct {
	Browser Browser `xml:"ECFRBRWS"`
}

type Browser struct {
	AmdDate string `xml:"AMDDATE"` // e.g. "Mar. 31, 2025"
	Div     Div    `xml:"DIV1"`    // root DIV1 (TITLE)
}

// ---------------------------------------------------------------------------
// Recursive <DIV#> blocks (DIV1–DIV9).  Each carries attributes + HEAD/TEXT.
// Children are captured generically so the same struct handles any depth.
// ---------------------------------------------------------------------------

type Div struct {
	XMLName xml.Name // DIV1, DIV2 … DIV9
	N       string   `xml:"N,attr"`    // “1”, “A”, “§ 1.1”, …
	Node    string   `xml:"NODE,attr"` // internal ID (don’t rely on)
	Type    string   `xml:"TYPE,attr"` // TITLE, CHAPTER, PART, SECTION…
	Head    string   `xml:"HEAD"`      // Human‑readable heading
	Text    *DivText `xml:"TEXT"`      // Optional
	// Any child DIVn nodes (any depth) land here:
	Children []Div `xml:",any"` // recursive
}

// Inside <TEXT> most of the interesting prose is paragraphs, lists, etc.
// We capture it as raw XML and let the caller post‑process if needed.
type DivText struct {
	Inner string `xml:",innerxml"`
}
