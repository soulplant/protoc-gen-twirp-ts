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
// - [ ] Factor service implementation better.
// - [ ] Parse dates, populate empty lists.

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

// MessageVisitor visits a message at a specific path.
type MessageVisitor func([]*d.DescriptorProto, *d.DescriptorProto)

// EnumVisitor visits an enum at a specific path.
type EnumVisitor func([]*d.DescriptorProto, *d.EnumDescriptorProto)

func walkType(mv MessageVisitor, ev EnumVisitor, parentTypes []*d.DescriptorProto, m *d.DescriptorProto) {
	mv(parentTypes, m)
	stack := append(parentTypes[:0:0], parentTypes...)
	stack = append(stack, m)
	for _, nt := range m.GetNestedType() {
		walkType(mv, ev, stack, nt)
	}
	for _, et := range m.GetEnumType() {
		ev(stack, et)
	}
}

// Gen stores state used during code generation.
type Gen struct {
	// Message map.
	m map[string]*d.DescriptorProto
	// Enum map.
	em map[string]*d.EnumDescriptorProto

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
		em: map[string]*d.EnumDescriptorProto{},
		pm: map[string]string{},
		lm: map[string]*d.SourceCodeInfo_Location{},
	}
}

func getTypeName(parentTypes []*d.DescriptorProto, name string) string {
	var parent []string
	for _, p := range parentTypes {
		parent = append(parent, p.GetName())
	}
	parent = append(parent, name)
	return strings.Join(parent, "_")
}

// RegisterType adds the given type to the global registry.
func (g *Gen) RegisterType(parentTypes []*d.DescriptorProto, t *d.DescriptorProto, e *d.EnumDescriptorProto, packageName string) {
	shortName := t.GetName()
	if shortName == "" {
		shortName = e.GetName()
		if shortName == "" {
			log.Fatalf("Either message or enum must be defined")
		} else {
			// log.Fatalf("short enum name: %s", shortName)
		}
	}
	name := packagePrefix(packageName) + getTypeName(parentTypes, shortName)
	if t != nil {
		_, ok := g.m[name]
		if ok {
			log.Fatalf("RegisterType: duplicate type detected: %s", name)
		}
		g.m[name] = t
	}
	if e != nil {
		_, ok := g.em[name]
		if ok {
			log.Fatalf("RegisterType: duplicate type detected: %s", name)
		}
		g.em[name] = e
	}
	g.pm[name] = packageName
	g.names = append(g.names, name)
}

// PathWalker handles traversing a location within a file.
type PathWalker struct {
	// Location we are traversing within fd.
	loc *d.SourceCodeInfo_Location
	// Where we are up to in the Location.
	idx int
}

// NewPathWalker creates a new PathWalker.
func NewPathWalker(loc *d.SourceCodeInfo_Location) *PathWalker {
	return &PathWalker{
		loc: loc,
	}
}

// Done is true when there are no more path segments.
func (pw *PathWalker) Done() bool {
	return pw.idx >= len(pw.loc.Path)
}

// Try advances the path and returns true if the next value matches.
func (pw *PathWalker) Try(next DescriptorField) bool {
	if pw.Done() {
		panic(fmt.Sprintf("Try %d: PathWalker is done", next))
	}
	if pw.loc.Path[pw.idx] == int32(next) {
		pw.idx++
		return true
	}
	return false
}

// Next advances the path and returns the next value in it.
func (pw *PathWalker) Next() int32 {
	result := pw.loc.Path[pw.idx]
	pw.idx++
	return result
}

// DescriptorField is the field number of a field in a descriptor proto. e.g.
// fileMessageType is the number of the "message_type" field in a
// FileDescriptorProto.
type DescriptorField int32

const (
	fileMessageType DescriptorField = 4
	fileEnumType    DescriptorField = 5
	fileService     DescriptorField = 6

	messageField      DescriptorField = 2
	messageNestedType DescriptorField = 3
	messageEnum       DescriptorField = 4

	serviceMethod DescriptorField = 2

	enumValue DescriptorField = 2
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

func locateEnum(e *d.EnumDescriptorProto, pw *PathWalker, c *cursor) string {
	if pw.Done() {
		return c.Current()
	}
	if pw.Try(enumValue) {
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
	if pw.Try(fileMessageType) {
		m := fd.MessageType[pw.Next()]
		c.Push(m.GetName())
		if pw.Done() {
			return c.Current()
		}
		for {
			if pw.Try(messageField) {
				num := pw.Next()
				f := m.GetField()[num]
				if pw.Done() {
					return c.CurrentMethod(f.GetName())
				}
				return ""
			}
			if pw.Try(messageNestedType) {
				m = m.GetNestedType()[pw.Next()]
				c.Push(m.GetName())
				if pw.Done() {
					return c.Current()
				}
				continue
			}
			if pw.Try(messageEnum) {
				e := m.GetEnumType()[pw.Next()]
				c.Push(e.GetName())
				return locateEnum(e, pw, c)
			}
			break
		}
	}
	if pw.Try(fileService) {
		s := fd.Service[pw.Next()]
		c.Push(s.GetName())
		if pw.Done() {
			return c.Current()
		}
		if pw.Try(serviceMethod) {
			m := s.GetMethod()[pw.Next()]
			if pw.Done() {
				return c.CurrentMethod(m.GetName())
			}
			return ""
		}
	}
	if pw.Try(fileEnumType) {
		e := fd.GetEnumType()[pw.Next()]
		c.Push(e.GetName())
		return locateEnum(e, pw, c)
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
	o.Printf("// tslint:disable\n\n")
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
	mv := func(path []*d.DescriptorProto, t *d.DescriptorProto) {
		var parent []string
		for _, p := range path {
			parent = append(parent, p.GetName())
		}
		g.RegisterType(path, t, nil, fd.GetPackage())
	}
	ev := func(path []*d.DescriptorProto, e *d.EnumDescriptorProto) {
		var parent []string
		for _, p := range path {
			parent = append(parent, p.GetName())
		}
		g.RegisterType(path, nil, e, fd.GetPackage())
	}
	for _, t := range fd.GetMessageType() {
		walkType(mv, ev, nil, t)
	}
	for _, et := range fd.GetEnumType() {
		ev(nil, et)
	}

	// o.Printf("opts %s\n", req.GetParameter())

	for _, name := range g.names {
		comment := ""
		if loc, ok := g.lm[name]; ok {
			comment = makeComment(loc.GetLeadingComments())
		}
		if t, ok := g.m[name]; ok {
			if t.GetOptions().GetMapEntry() {
				continue
			}
			o.Printf(comment)
			o.Printf("export interface %s {\n", name)
			for _, f := range t.GetField() {
				fname := name + "." + f.GetName()
				comment := ""
				comTrail := ""
				if loc, ok := g.lm[fname]; ok {
					comment = makeComment(loc.GetLeadingComments())
					comTrail = strings.TrimRight(makeComment(loc.GetTrailingComments()), "\n")
					if comTrail != "" {
						comTrail = "  " + comTrail
					}
				}
				o.Printf(indentLines(1, fmt.Sprintf("%s%s?: %s;%s", comment, f.GetJsonName(), g.GetTypeName(f), comTrail)))
				o.Printf("\n")
			}
			o.Printf("};\n\n")
		}
		if e, ok := g.em[name]; ok {
			o.Printf("export enum %s {\n", name)
			for _, v := range e.GetValue() {
				vcomTrail := ""
				if loc, ok := g.lm[name+"."+v.GetName()]; ok {
					vcom := strings.TrimRight(makeComment(loc.GetLeadingComments()), "\n")
					if vcom != "" {
						o.Printf("  " + vcom + "\n")
					}
					vcomTrail = "  " + strings.TrimRight(makeComment(loc.GetTrailingComments()), "\n")
				}
				o.Printf("  %s = \"%s\",%s\n", v.GetName(), v.GetName(), vcomTrail)
			}
			o.Printf("}\n")
		}
	}

	for _, s := range fd.GetService() {
		o.Printf("export class %s%s {\n", s.GetName(), packagePrefix(fd.GetPackage()))
		o.Printf("  constructor(private baseUrl: string, private f: typeof fetch) {}\n")
		for _, m := range s.GetMethod() {
			mName := packagePrefix(fd.GetPackage()) + s.GetName() + "." + m.GetName()
			loc, _ := g.lm[mName]
			comment := ""
			if loc.GetLeadingComments() != "" {
				comment = makeComment(loc.GetLeadingComments())
			}
			url := "/twirp/" + fd.GetPackage() + "." + s.GetName() + "/" + m.GetName()
			body := indentLines(1, strings.Join([]string{
				fmt.Sprintf(`return this.f(this.baseUrl + "%s", {method: "POST", body: JSON.stringify(req), headers: {"Content-Type": "application/json"}}).then(response => {`, url),
				`  if (response.status >= 200 && response.status < 300) {`,
				`    return response.json();`,
				`  }`,
				`  throw response`,
				`});`,
				"",
			}, "\n"))
			mname := strings.ToLower(m.GetName()[0:1]) + m.GetName()[1:]
			def := fmt.Sprintf("%s(req: %s): Promise<%s> {\n%s}\n", mname, qualifiedToCanonical(m.GetInputType()), qualifiedToCanonical(m.GetOutputType()), body)
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
	rawType := g.getRawTypeName(f)
	if m, ok := g.m[rawType]; ok {
		if m.GetOptions().GetMapEntry() {
			keyType := g.GetTypeName(m.GetField()[0])
			valueType := g.GetTypeName(m.GetField()[1])
			return fmt.Sprintf(`{[key: %s]: %s}`, keyType, valueType)
		}
	}
	if f.GetLabel() == d.FieldDescriptorProto_LABEL_REPEATED {
		return rawType + "[]"
	}
	return rawType
}

func (g *Gen) getRawTypeName(f *d.FieldDescriptorProto) string {
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
	default:
		panic(fmt.Sprintf("GetTypeName: unknown type %s", f.GetType()))
	}
}

func wellKnownToTS(typeName string) string {
	switch typeName {
	case ".google.protobuf.Timestamp":
		return "Date"
	case ".google.protobuf.Struct":
		return "{}"
	case ".google.protobuf.FieldMask":
		return "{ paths: string[] }"
	case ".google.protobuf.DoubleValue":
		fallthrough
	case ".google.protobuf.Int32Value":
		fallthrough
	case ".google.protobuf.UInt32Value":
		fallthrough
	case ".google.protobuf.FloatValue":
		return "number | null"
	case ".google.protobuf.Int64Value":
		fallthrough
	case ".google.protobuf.UInt64Value":
		return "string | null"
	case ".google.protobuf.BoolValue":
		return "boolean | null"
	case ".google.protobuf.StringValue":
		fallthrough
	case ".google.protobuf.BytesValue":
		return "string | null"
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
