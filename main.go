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
	buf bytes.Buffer
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

	o := &bytes.Buffer{}

	for _, f := range req.GetProtoFile() {
		fmt.Fprintf(o, "file: %s\n", f.GetName())
		for _, t := range f.GetMessageType() {
			walkType(func(path []*descriptor.DescriptorProto, t *descriptor.DescriptorProto) {
				spacing := "  "
				var parent []string
				for _, p := range path {
					spacing += "  "
					parent = append(parent, p.GetName())
				}
				fmt.Fprintf(o, "%s(%s) type: %s\n", spacing, strings.Join(parent, "."), t.GetName())
			}, nil, t)
		}
		for _, s := range f.GetService() {
			fmt.Fprintf(o, "service %s\n", s.GetName())
			for _, m := range s.GetMethod() {
				fmt.Fprintf(o, "  %s\n", m.GetName())
			}
		}
		fmt.Fprintln(o)
	}

	fmt.Fprintf(o, "opts %s\n", req.GetParameter())

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
