package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"log"
	"path"
	"text/template"
	// "fmt"
)

// read all contents of a zip.File and exit on error
func readZipFile(f *zip.File) []byte {
	rc, err := f.Open()
	if err != nil {
		log.Fatal(err)
	}
	defer rc.Close()
	buf, err := io.ReadAll(rc)
	if err != nil {
		log.Fatal(err)
	}
	return buf
}

type Item struct {
	Properties string `xml:"properties,attr"`
	Id         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
}
type Itemref struct {
	Idref string `xml:"idref,attr"`
}
type Meta struct {
	Attrs   []xml.Attr `xml:",any,attr"`
	Content []string   `xml:",innerhtml"`
}
type Opf struct {
	XMLName      xml.Name `xml:"package"`
	Version      string   `xml:"version,attr"`
	UniqueId     string   `xml:"unique-identifier,attr"`
	Identifiers  []string `xml:"metadata>identifier"`
	Titles       []string `xml:"metadata>title"`
	Creators     []string `xml:"metadata>creator"`
	Subjects     []string `xml:"metadata>subject"`
	Descriptions []string `xml:"metadata>description"`
	Languages    []string `xml:"metadata>language"`
	Metas        []Meta   `xml:"metadata>meta"`
	Manifest     []Item   `xml:"manifest>item"`
	Spine        struct {
		Itemrefs []Itemref `xml:"itemref"`
		// epub 2 ncx id
		Toc string `xml:"toc,attr"`
	} `xml:"spine"`
}

// parse opf/package document
func parseOpf(f *zip.File) Opf {
	buf := readZipFile(f)
	var opf Opf
	if err := xml.Unmarshal(buf, &opf); err != nil {
		log.Fatal(err)
	}
	return opf
}

type Link struct {
	Href     string `xml:"href,attr"`
	Contents string `xml:",innerxml"`
}
type ListItem struct {
	// XMLName xml.Name `xml:"li"`
	Link Link       `xml:"a"`
	Span string     `xml:"span"`
	List []ListItem `xml:"ol>li"`
}
type Nav struct {
	// XMLName xml.Name   `xml:"nav"`
	Type string     `xml:"type,attr"`
	List []ListItem `xml:"ol>li"`
}
type NavHtml struct {
	basePath string
	XMLName  xml.Name `xml:"html"`
	Nav      Nav      `xml:"body>nav"`
}

// parse navigation document
func parseNav(f *zip.File) []byte {
	buf := readZipFile(f)
	nav_html := NavHtml{basePath: path.Dir(f.Name)}
	if err := xml.Unmarshal(buf, &nav_html); err != nil {
		log.Fatal(err)
	}
	return nav_html.printHtml()
}

func (n NavHtml) printHtml() []byte {
	wr := new(bytes.Buffer)
	if err := template.Must(template.New("nav").
		Funcs(map[string]any{"absolutePath": func(p string) string { return absolutePath(n.basePath, p) }}).Parse(`
{{block "list" .List}}
<ol>
{{range $item := .}}
{{if $item.Span}}<li><span>{{$item.Span}}</span></li>{{end}}
{{if $item.Link}}<li><a href="{{$item.Link.Href | absolutePath}}">{{$item.Link.Contents}}</a></li>{{end}}
{{if $item.List}}{{template "list" $item.List}}{{end}}
{{end}}
</ol>
{{end}}
`)).Execute(wr, n.Nav); err != nil {
		log.Fatal(err)
	}
	return wr.Bytes()
}

type NavPoint struct {
	// XMLName xml.Name `xml:"navPoint"`
	Content struct {
		Src string `xml:"src,attr"`
	} `xml:"content"`
	Label string     `xml:"navLabel>text"`
	List  []NavPoint `xml:"navPoint"`
}
type Ncx struct {
	XMLName  xml.Name   `xml:"ncx"`
	List     []NavPoint `xml:"navMap>navPoint"`
	basePath string
}

func (n Ncx) toNavHtml() NavHtml {
	list := make([]ListItem, 0)
	for _, point := range n.List {
		list = append(list, point.toListItem())
	}
	return NavHtml{Nav: Nav{Type: "toc", List: list}, basePath: n.basePath}
}
func (n NavPoint) toListItem() ListItem {
	list := make([]ListItem, 0)
	for _, point := range n.List {
		list = append(list, point.toListItem())
	}
	if n.Content.Src == "" {
		return ListItem{Span: n.Label, List: list}
	}
	return ListItem{
		Link: Link{Href: n.Content.Src, Contents: n.Label},
		List: list,
	}
}

// parse navigation document(ncx)
func parseNcx(f *zip.File) []byte {
	buf := readZipFile(f)
	ncx := Ncx{basePath: path.Dir(f.Name)}
	if err := xml.Unmarshal(buf, &ncx); err != nil {
		log.Fatal(err)
	}
	nav_html := ncx.toNavHtml()

	return nav_html.printHtml()
}
