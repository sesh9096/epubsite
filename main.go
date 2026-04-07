package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"text/template"
)

var cmdline_args CmdlineArgs

// go run main.go xhtml_editor.go ~/extern/books/a-regressors-path-to-cultivation.epub

// a demonstration that epub files are just websites
func main() {
	// exampleE()
	parseCmdline()
	if len(os.Args) < 2 {
		exitHelp(1)
	}
	// fmt.Printf("assetdir: %s\n", assetdir)
	// Open a epub for reading.
	book := OpenEpub(cmdline_args.epub_path)
	defer book.Close()
	address := ":" + cmdline_args.port
	server := http.Server{Addr: address, Handler: book}
	fmt.Printf("http://localhost%s\n", address)
	server.ListenAndServe()
}

type Epub struct {
	*zip.ReadCloser
	// key is filename starting at root without preceding '/'
	Files    map[string]File
	Homepage []byte
	Nav      []byte
}
type File struct {
	mimetype string
	*zip.File
	prev string
	next string
}

func OpenEpub(filename string) Epub {
	r, err := zip.OpenReader(filename)
	if err != nil {
		log.Fatal(err)
	}

	// Iterate through the files in the archive,
	// printing some of their contents.
	var opf_path string
	ret := Epub{Files: make(map[string]File)}
	for _, f := range r.File {
		ret.Files[f.Name] = File{File: f}
		if f.Name == "mimetype" {
			buf := readZipFile(f)
			if string(buf) != "application/epub+zip" {
				log.Fatal("Error: mimetype invalid for epub")
			}
		} else if f.Name == "META-INF/container.xml" {
			buf := readZipFile(f)
			type RootFile struct {
				FullPath  string `xml:"full-path,attr"`
				MediaType string `xml:"media-type,attr"`
			}
			var metainf struct {
				XMLName  xml.Name `xml:"container"`
				RootFile RootFile `xml:"rootfiles>rootfile"`
			}
			if err := xml.Unmarshal(buf, &metainf); err != nil {
				log.Fatal(err)
			}
			opf_path = metainf.RootFile.FullPath
			if metainf.RootFile.MediaType != "application/oebps-package+xml" {
				log.Fatal("Invalid root file mimetype: %s", "application/oebps-package+xml")
			}
		}
	}
	prefix := path.Dir(opf_path)

	opf := parseOpf(ret.Files[opf_path].File)
	id_map := make(map[string]string)
	var cover_image, nav_path string
	for _, item := range opf.Manifest {
		item.Href = absolutePath(prefix, item.Href)
		ret.Files[item.Href] = File{File: ret.Files[item.Href].File, mimetype: item.MediaType}
		id_map[item.Id] = item.Href
		if strings.Contains(item.Properties, "nav") {
			nav_path = item.Href
		}
		if strings.Contains(item.Properties, "cover-image") {
			cover_image = item.Href
		}
		// if item.Properties contains "cover-image"
	}
	p := id_map[opf.Spine.Itemrefs[0].Idref]
	for _, itemref := range opf.Spine.Itemrefs[1:] {
		n := id_map[itemref.Idref]
		{
			f := ret.Files[n]
			f.prev = p
			ret.Files[n] = f
		}
		{
			f := ret.Files[p]
			f.next = n
			ret.Files[p] = f
		}
		// fmt.Printf("p: %v, c: %v, n: %v\n", ret.Files[p].prev, p, ret.Files[p].next)
		// fmt.Printf("file: %v\n", ret.Files[p])
		p = n
	}

	var nav []byte
	if opf.Version[0] == '2' {
		nav = parseNcx(ret.Files[id_map[opf.Spine.Toc]].File)
		// cover image
	outer:
		for _, tag := range opf.Metas {
			for _, attr := range tag.Attrs {
				if attr.Name.Local == "name" && attr.Value == "cover" {
					for _, attr := range tag.Attrs {
						if attr.Name.Local == "content" {
							cover_image = id_map[attr.Value]
						}
					}
					break outer
				}
			}
		}
	} else if opf.Version[0] == '3' {
		// parse navigation document
		nav = parseNav(ret.Files[nav_path].File)
	} else {
		log.Fatal("Error, unknown epub version")
	}

	wr := new(bytes.Buffer)
	if err = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html><head>
<title>{{index .Titles 0}}</title>
<link href="/epubsite-styles.css" rel="stylesheet" type="text/css"/>
<meta charset="utf-8"/>
</head>
<body>
<div class="home-content">
<div class="home-column-left">
<img src="{{.CoverImage}}" class="home-cover"/>
{{range $creator := .Creators}}<p>Author: {{$creator}}</p>{{end}}
</div>
<div class="home-column-right">
<h2>{{index .Titles 0}}</h2>
{{range $title := slice .Titles 1}}<h3>{{$title}}</h3>{{end}}
{{range $subject := .Subjects}}<p>Subject: {{$subject}}</p>{{end}}
{{range $description := .Descriptions}}<p>{{$description}}</p>{{end}}
<h3>Chapters</h3>
<nav id="home-toc">{{.Nav}}</nav>
</div>
</div>
</body></html>
`)).Execute(wr, struct {
		Nav string
		Opf
		CoverImage string
	}{Opf: opf, CoverImage: cover_image, Nav: string(nav)}); err != nil {
		log.Fatal(err)
	}
	ret.Homepage = wr.Bytes()

	return ret
}
func (e Epub) Close() {
	e.ReadCloser.Close()
}
func (e Epub) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// req.URL.Path[1:] == '/
	p := req.URL.Path[1:]
	file, ok := e.Files[p]
	if ok {
		// fmt.Printf("file: %v\n", file)
		// fmt.Printf("type: %s", file.Mimetype)
		w.Header().Set("Content-Type", file.mimetype+"; charset=utf-8") // normal header
		rc, err := file.Open()
		if err != nil {
			log.Fatal(err)
		}
		defer rc.Close()
		if file.mimetype == "application/xhtml+xml" {
			var buf []byte
			buf, err = io.ReadAll(rc)
			if err != nil {
				log.Fatal(err)
			}
			type Container struct {
				// XMLName xml.Name   `xml:"nav"`
				Attrs   []xml.Attr `xml:",any,attr"`
				Content string     `xml:",innerxml"`
			}
			type Html struct {
				XMLName xml.Name   `xml:"html"`
				Attrs   []xml.Attr `xml:",any,attr"`
				Head    Container  `xml:"head"`
				Body    Container  `xml:"body"`
			}
			var html Html
			if err = xml.Unmarshal(buf, &html); err != nil {
				log.Fatal(err)
			}
			// fmt.Printf("attrs: %v\n", html.Attrs)
			nav_links := fmt.Sprintf(`
<nav class="epubsite-nav"><a class="previous-link" href="/%s" title="key: p">Previous</a><a class="home-link" href="/" title="key: u">Home</a><a class="next-link" href="/%s" title="key: n">Next</a></nav>
`, file.prev, file.next)
			for i, a := range html.Attrs {
				if a.Name.Space == "xmlns" {
					html.Attrs[i].Name = xml.Name{Local: a.Name.Space + ":" + a.Name.Local}
				}
			}
			html.Head.Content += fmt.Sprintf(`
<link href="/epubsite-styles.css" rel="stylesheet" type="text/css"/>
<script>
document.addEventListener("keydown", function (event) {
	if (event.key == "p") {
		event.preventDefault()
		window.location = "/%s"
	} else if (event.key == "u") {
		event.preventDefault()
		window.location = "/"
	} else if (event.key == "n") {
		event.preventDefault()
		window.location = "/%s"
	}
})
</script>
`, file.prev, file.next)
			html.Body.Content = nav_links + html.Body.Content + nav_links
			buf, err = xml.Marshal(html)
			if err != nil {
				log.Fatal(err)
			}
			w.Write([]byte(xml.Header))
			w.Write(buf)
		} else {
			io.CopyN(w, rc, int64(file.UncompressedSize64))
		}
	} else {
		if p == "" {
			w.Write(e.Homepage)
		} else if rc, err := os.Open(cmdline_args.assetdir + p); err == nil {
			defer rc.Close()
			mt := mime.TypeByExtension(path.Ext(p))
			// fmt.Printf("mimetype: %s\n", mt)
			w.Header().Set("Content-Type", mt+"; charset=utf-8") // normal header
			_, err = io.Copy(w, rc)
			if err != nil {
				log.Fatal(err)
			}
		} else {
			w.WriteHeader(404)
			w.Write([]byte("file not found"))
		}
	}
}

func absolutePath(dir, p string) string {
	if path.IsAbs(p) {
		return p
	} else {
		return path.Join(dir, p)
	}
}

func exitHelp(status int) {
	fmt.Printf(
		`Usage: %s <path/to/epub>

Start a web server serving an epub file as a website
`, os.Args[0])
	os.Exit(status)
}

type CmdlineArgs struct {
	assetdir  string
	port      string
	epub_path string
}

func (f *CmdlineArgs) Parse() {
	_, assetdir, _, _ := runtime.Caller(0)
	assetdir = path.Dir(assetdir) + "/assets/"
	flag.StringVar(&f.assetdir, "assetdir", assetdir, "help message for flagname")
	flag.StringVar(&f.port, "port", "19234", "port to listen on for the webserver")
	flag.Parse()
	f.epub_path = flag.Arg(0)
}

func parseCmdline() *CmdlineArgs {
	cmdline_args.Parse()
	return &cmdline_args
}
