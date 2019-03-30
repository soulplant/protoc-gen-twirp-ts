package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"strings"

	d "github.com/golang/protobuf/protoc-gen-go/descriptor"

	"github.com/golang/protobuf/proto"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
)

type file struct {
	buf *bytes.Buffer
}

func (f file) Printf(format string, args ...interface{}) {
	fmt.Fprintf(f.buf, format, args...)
}

func (f file) String() string {
	return f.buf.String()
}

func makeFile(name, content string) *plugin.CodeGeneratorResponse_File {
	return &plugin.CodeGeneratorResponse_File{
		Name:    &name,
		Content: &content,
	}
}

func walkType(f func(parentTypes []*d.DescriptorProto, t *d.DescriptorProto), parentTypes []*d.DescriptorProto, t *d.DescriptorProto) {
	f(parentTypes, t)
	stack := append(parentTypes[:0:0], parentTypes...)
	stack = append(stack, t)
	for _, nt := range t.GetNestedType() {
		walkType(f, stack, nt)
	}
}

// Gen stores state used during code generation.
type Gen struct {
	m map[string]*d.DescriptorProto

	// Package names keyed by type.
	pm map[string]string

	// Locations keyed by type.
	lm    map[string]*d.SourceCodeInfo_Location
	names []string
}

// NewGen returns a new Gen.
func NewGen() *Gen {
	return &Gen{
		m:  map[string]*d.DescriptorProto{},
		pm: map[string]string{},
		lm: map[string]*d.SourceCodeInfo_Location{},
	}
}

func getTypeName(parentTypes []*d.DescriptorProto, t *d.DescriptorProto) string {
	var parent []string
	for _, p := range parentTypes {
		parent = append(parent, p.GetName())
	}
	parent = append(parent, t.GetName())
	return strings.Join(parent, "_")
}

// RegisterType adds the given type to the global registry.
func (g *Gen) RegisterType(parentTypes []*d.DescriptorProto, t *d.DescriptorProto, packageName string) {
	name := packagePrefix(packageName) + getTypeName(parentTypes, t)
	_, ok := g.m[name]
	if ok {
		log.Fatalf("RegisterType: duplicate type detected: %s", name)
	}
	g.m[name] = t
	g.pm[name] = packageName
	g.names = append(g.names, name)
}

// type LocationTree struct {
// 	nodes []*LocationNode
// }

// func getTypeNameByPath(path []int32, fd *d.FileDescriptorProto) string {
// 	fd.
// }

// pathWalker handles traversing a location within a file.
type pathWalker struct {
	// Location we are traversing within fd.
	loc *d.SourceCodeInfo_Location
	// Where we are up to in the Location.
	idx int
}

func NewPathWalker(loc *d.SourceCodeInfo_Location) *pathWalker {
	return &pathWalker{
		loc: loc,
	}
}

func (pw *pathWalker) Done() bool {
	return pw.idx >= len(pw.loc.Path)
}

func (pw *pathWalker) Try(next int32) bool {
	if pw.Done() {
		panic(fmt.Sprintf("Try %d: pathWalker is done", next))
	}
	if pw.loc.Path[pw.idx] == next {
		pw.idx++
		return true
	}
	return false
}

func (pw *pathWalker) Next() int32 {
	result := pw.loc.Path[pw.idx]
	pw.idx++
	return result
}

type DescriptorField int32

const (
	Field_File_MessageType = 4
	Field_File_Service     = 6

	Field_Message_Field      = 2
	Field_Message_NestedType = 3
)

func packagePrefix(pack string) string {
	return strings.ToUpper(pack[0:1]) + pack[1:]
}

func locateInFile(fd *d.FileDescriptorProto, loc *d.SourceCodeInfo_Location) string {
	pw := NewPathWalker(loc)
	// p := loc.GetPath()
	// o.Printf("// path = %v\n", p)
	if pw.Done() {
		return ""
	}
	if pw.Try(Field_File_MessageType) {
		m := fd.MessageType[pw.Next()]
		name := packagePrefix(fd.GetPackage()) + m.GetName()
		if pw.Done() {
			return packagePrefix(fd.GetPackage()) + m.GetName()
		}
		if pw.Try(Field_Message_Field) {
			num := pw.Next()
			f := m.GetField()[num]
			name += "." + f.GetName()
			if pw.Done() {
				return name
			}
			return ""
		}
		if pw.Try(Field_Message_NestedType) {
			// TODO(james): Implement.
			return ""
		}
		return ""
	}
	return ""
}

func makeComment(comment string) string {
	leading := strings.TrimRight(comment, "\n")
	if leading == "" {
		return ""
	}
	return "//" + strings.Join(strings.Split(leading, "\n"), "\n//") + "\n"
}

func indentLines(n int, lines string) string {
	parts := strings.Split(lines, "\n")
	for i := range parts {
		for j := 0; j < n; j++ {
			parts[i] = "  " + parts[i]
		}
	}
	return strings.Join(parts, "\n")
}

// Generate a response.
func (g *Gen) Generate(fd *d.FileDescriptorProto) *plugin.CodeGeneratorResponse {
	o := &file{buf: bytes.NewBufferString("")}
	for _, loc := range fd.GetSourceCodeInfo().GetLocation() {
		name := locateInFile(fd, loc)
		if name == "" {
			continue
		}
		g.lm[name] = loc
	}
	for _, t := range fd.GetMessageType() {
		walkType(func(path []*d.DescriptorProto, t *d.DescriptorProto) {
			spacing := "  "
			var parent []string
			for _, p := range path {
				spacing += "  "
				parent = append(parent, p.GetName())
			}
			g.RegisterType(path, t, fd.GetPackage())
		}, nil, t)
	}

	// o.Printf("opts %s\n", req.GetParameter())

	for _, name := range g.names {
		t := g.m[name]
		comment := ""
		if loc, ok := g.lm[name]; ok {
			comment = makeComment(loc.GetLeadingComments())
		}
		o.Printf(comment)
		o.Printf("export type %s = {\n", name)
		for _, f := range t.GetField() {
			fname := name + "." + f.GetName()
			comment := ""
			if loc, ok := g.lm[fname]; ok {
				comment = makeComment(loc.GetLeadingComments())
			}
			o.Printf(indentLines(1, fmt.Sprintf("%s%s?: %s;", comment, f.GetName(), g.GetTypeName(f))))
			o.Printf("\n")
		}
		o.Printf("};\n")
	}

	for _, s := range fd.GetService() {
		o.Printf("service %s\n", s.GetName())
		for _, m := range s.GetMethod() {
			o.Printf("  %s\n", m.GetName())
		}
	}
	o.Printf("\n")

	res := &plugin.CodeGeneratorResponse{
		File: []*plugin.CodeGeneratorResponse_File{
			makeFile("out.txt", o.String()),
		},
	}
	return res
}

// GetTypeName of the given field.
func (g *Gen) GetTypeName(f *d.FieldDescriptorProto) string {
	switch f.GetType() {
	case d.FieldDescriptorProto_TYPE_INT32:
		fallthrough
	case d.FieldDescriptorProto_TYPE_FIXED32:
		fallthrough
	case d.FieldDescriptorProto_TYPE_FIXED64:
		fallthrough
	case d.FieldDescriptorProto_TYPE_FLOAT:
		fallthrough
	case d.FieldDescriptorProto_TYPE_SFIXED32:
		fallthrough
	case d.FieldDescriptorProto_TYPE_SFIXED64:
		fallthrough
	case d.FieldDescriptorProto_TYPE_UINT32:
		fallthrough
	case d.FieldDescriptorProto_TYPE_DOUBLE:
		fallthrough
	case d.FieldDescriptorProto_TYPE_SINT32:
		return "number"
	case d.FieldDescriptorProto_TYPE_SINT64:
		fallthrough
	case d.FieldDescriptorProto_TYPE_UINT64:
		fallthrough
	case d.FieldDescriptorProto_TYPE_INT64:
		return "string"
	case d.FieldDescriptorProto_TYPE_STRING:
		return "string"
	case d.FieldDescriptorProto_TYPE_BOOL:
		return "boolean"
	case d.FieldDescriptorProto_TYPE_MESSAGE:
		pieces := strings.Split(f.GetTypeName(), ".")[1:]
		packageName := strings.ToUpper(pieces[0][0:1]) + pieces[0][1:]
		typeName := strings.Join(pieces[1:], "_")
		return packageName + typeName
	default:
		panic(fmt.Sprintf("GetTypeName: unknown type %s", f.GetType()))
	}
}

func main() {
	buf, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("No input: %s", err)
	}

	var req plugin.CodeGeneratorRequest
	if err := proto.Unmarshal(buf, &req); err != nil {
		log.Fatalf("Unmarshal: %s\n", err)
	}

	g := NewGen()
	res := g.Generate(req.GetProtoFile()[0])
	outBuf, err := proto.Marshal(res)
	if err != nil {
		log.Fatalf("Marshal: %s\n", err)
	}
	io.Copy(os.Stdout, bytes.NewBuffer(outBuf))
}
