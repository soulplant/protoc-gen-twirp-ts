package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"github.com/golang/protobuf/protoc-gen-go/descriptor"

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

func walkType(f func(parentTypes []*descriptor.DescriptorProto, t *descriptor.DescriptorProto), parentTypes []*descriptor.DescriptorProto, t *descriptor.DescriptorProto) {
	f(parentTypes, t)
	stack := append(parentTypes[:0:0], parentTypes...)
	stack = append(stack, t)
	for _, nt := range t.GetNestedType() {
		walkType(f, stack, nt)
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

	o := &file{buf: bytes.NewBufferString("")}

	for _, f := range req.GetProtoFile() {
		for _, t := range f.GetMessageType() {
			walkType(func(path []*descriptor.DescriptorProto, t *descriptor.DescriptorProto) {
				spacing := "  "
				var parent []string
				for _, p := range path {
					spacing += "  "
					parent = append(parent, p.GetName())
				}
				o.Printf("%s(%s) type: %s\n", spacing, strings.Join(parent, "."), t.GetName())
			}, nil, t)
		}
		for _, s := range f.GetService() {
			o.Printf("service %s\n", s.GetName())
			for _, m := range s.GetMethod() {
				o.Printf("  %s\n", m.GetName())
			}
		}
		o.Printf("\n")
	}

	o.Printf("opts %s\n", req.GetParameter())

	res := &plugin.CodeGeneratorResponse{
		File: []*plugin.CodeGeneratorResponse_File{
			makeFile("out.txt", o.String()),
		},
	}
	outBuf, err := proto.Marshal(res)
	if err != nil {
		log.Fatalf("Marshal: %s\n", err)
	}
	io.Copy(os.Stdout, bytes.NewBuffer(outBuf))
}
