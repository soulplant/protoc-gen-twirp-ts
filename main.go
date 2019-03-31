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

// TODO
// - [ ] Implement service
// - [ ] Implement enums
// - [ ] Emit camelCase names

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

func walkType(f func(parentTypes []*d.DescriptorProto, m *d.DescriptorProto), parentTypes []*d.DescriptorProto, m *d.DescriptorProto) {
	f(parentTypes, m)
	stack := append(parentTypes[:0:0], parentTypes...)
	stack = append(stack, m)
	for _, nt := range m.GetNestedType() {
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
	File_MessageType = 4
	File_EnumType    = 5
	File_Service     = 6

	Message_Field      = 2
	Message_NestedType = 3
	Message_Enum       = 4

	Service_Method = 2

	Enum_Value = 2
)

func packagePrefix(pack string) string {
	return strings.ToUpper(pack[0:1]) + pack[1:]
}

type cursor struct {
	// Name of the package we are in.
	pkg string

	// Names of the types that we are in.
	typeNames []string
}

func (c *cursor) Push(name string) {
	c.typeNames = append(c.typeNames, name)
}

func (c *cursor) Pop() {
	c.typeNames = c.typeNames[0 : len(c.typeNames)-1]
}

func (c *cursor) Current() string {
	return packagePrefix(c.pkg) + strings.Join(c.typeNames, "_")
}

func (c *cursor) CurrentMethod(method string) string {
	return c.Current() + "." + method
}

func locateEnum(e *d.EnumDescriptorProto, pw *pathWalker, c *cursor) string {
	if pw.Done() {
		return c.Current()
	}
	if pw.Try(Enum_Value) {
		v := e.GetValue()[pw.Next()]
		if pw.Done() {
			return c.CurrentMethod(v.GetName())
		}
	}
	return ""
}

func locateInFile(fd *d.FileDescriptorProto, loc *d.SourceCodeInfo_Location) string {
	pw := NewPathWalker(loc)
	if pw.Done() {
		return ""
	}
	c := &cursor{
		pkg: fd.GetPackage(),
	}
	if pw.Try(File_MessageType) {
		m := fd.MessageType[pw.Next()]
		c.Push(m.GetName())
		if pw.Done() {
			return c.Current()
		}
		for {
			if pw.Try(Message_Field) {
				num := pw.Next()
				f := m.GetField()[num]
				if pw.Done() {
					return c.CurrentMethod(f.GetName())
				}
				return ""
			}
			if pw.Try(Message_NestedType) {
				m = m.GetNestedType()[pw.Next()]
				c.Push(m.GetName())
				if pw.Done() {
					return c.Current()
				}
				continue
			}
			if pw.Try(Message_Enum) {
				e := m.GetEnumType()[pw.Next()]
				c.Push(e.GetName())
				return locateEnum(e, pw, c)
			}
			break
		}
		if pw.Try(File_EnumType) {
			e := m.GetEnumType()[pw.Next()]
			c.Push(e.GetName())
			return locateEnum(e, pw, c)
		}
	}
	if pw.Try(File_Service) {
		s := fd.Service[pw.Next()]
		c.Push(s.GetName())
		if pw.Done() {
			return c.Current()
		}
		if pw.Try(Service_Method) {
			m := s.GetMethod()[pw.Next()]
			if pw.Done() {
				return c.CurrentMethod(m.GetName())
			}
			return ""
		}
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

func qualifiedToCanonical(typeName string) string {
	pieces := strings.Split(typeName[1:], ".")
	return packagePrefix(pieces[0]) + strings.Join(pieces[1:], "_")
}

// Generate a response.
func (g *Gen) Generate(fd *d.FileDescriptorProto) *plugin.CodeGeneratorResponse {
	o := &file{buf: bytes.NewBufferString("")}
	for _, loc := range fd.GetSourceCodeInfo().GetLocation() {
		name := locateInFile(fd, loc)
		if name == "" {
			// o.Printf("// No info for %v\n", loc)
			continue
		}
		// o.Printf("// Adding info for %s\n", name)
		// o.Printf("//    %v\n", loc)
		if oldLoc, ok := g.lm[name]; ok {
			log.Fatalf("clobbering %s loc %v with %v", name, oldLoc, loc)
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
		o.Printf("};\n\n")
	}

	for _, s := range fd.GetService() {
		o.Printf("export interface %s {\n", s.GetName())
		for _, m := range s.GetMethod() {
			mName := packagePrefix(fd.GetPackage()) + s.GetName() + "." + m.GetName()
			loc, _ := g.lm[mName]
			comment := ""
			if loc.GetLeadingComments() != "" {
				comment = makeComment(loc.GetLeadingComments())
			}
			def := fmt.Sprintf("%s(req: %s): Promise<%s>;\n\n", m.GetName(), qualifiedToCanonical(m.GetInputType()), qualifiedToCanonical(m.GetOutputType()))
			o.Printf(indentLines(1, strings.TrimRight(comment+def, "\n")) + "\n\n")
		}
		o.Printf("}\n\n")
	}
	o.Printf("\n")

	res := &plugin.CodeGeneratorResponse{
		File: []*plugin.CodeGeneratorResponse_File{
			makeFile("out.ts", o.String()),
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
		wk := wellKnownToTS(f.GetTypeName())
		if wk != "" {
			return wk
		}
		return qualifiedToCanonical(f.GetTypeName())
	case d.FieldDescriptorProto_TYPE_ENUM:
		return qualifiedToCanonical(f.GetTypeName())
		// return "number /* fix me */"
	default:
		panic(fmt.Sprintf("GetTypeName: unknown type %s", f.GetType()))
	}
}

func wellKnownToTS(typeName string) string {
	switch typeName {
	case ".google.protobuf.Timestamp":
		return "string"
	case ".google.protobuf.Struct":
		return "{}"
	case ".google.protobuf.FieldMask":
		return "string[]"
	default:
		return ""
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

	for _, f := range req.GetProtoFile() {
		if strings.HasPrefix(f.GetName(), "google/protobuf") {
			continue
		}
		g := NewGen()
		res := g.Generate(f)
		outBuf, err := proto.Marshal(res)
		if err != nil {
			log.Fatalf("Marshal: %s\n", err)
		}
		io.Copy(os.Stdout, bytes.NewBuffer(outBuf))
	}
}
