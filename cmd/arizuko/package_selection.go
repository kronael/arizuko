package main

import (
	"bytes"
	"fmt"
	"strconv"

	"github.com/BurntSushi/toml"
	"github.com/kronael/arizuko/container"
	"github.com/pelletier/go-toml/v2/unstable"
)

type productEdit struct {
	header unstable.Range
	source unstable.Range
	field  unstable.Range
	extra  bool
}

func editProductSelection(data []byte, index int, source string) ([]byte, error) {
	var manifest container.ProductManifest
	if _, err := toml.Decode(string(data), &manifest); err != nil {
		return nil, err
	}
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(data)
	var edits []productEdit
	current := -1
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind == unstable.Comment {
			continue
		}
		var keys []string
		for key := node.Key(); key.Next(); {
			keys = append(keys, string(key.Node().Data))
		}
		isProduct := len(keys) > 0 && keys[0] == "product"
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			current = -1
			if !isProduct {
				continue
			}
			if node.Kind != unstable.ArrayTable || len(keys) != 1 {
				return nil, fmt.Errorf("selection edits require plain [[product]] tables")
			}
			key := node.Child().Raw
			start := bytes.LastIndexByte(data[:key.Offset], '\n') + 1
			end := int(key.Offset + key.Length)
			for end < len(data) && (data[end] == ' ' || data[end] == '\t') {
				end++
			}
			if end+2 > len(data) || string(data[end:end+2]) != "]]" {
				return nil, fmt.Errorf("unsupported product table header")
			}
			edits = append(edits, productEdit{header: unstable.Range{
				Offset: uint32(start), Length: uint32(end + 2 - start),
			}})
			current = len(edits) - 1
		case unstable.KeyValue:
			if current < 0 {
				if isProduct {
					return nil, fmt.Errorf("selection edits require plain [[product]] tables")
				}
				continue
			}
			if len(keys) != 1 || keys[0] != "source" || node.Value().Kind != unstable.String {
				edits[current].extra = true
				continue
			}
			edits[current].source = node.Value().Raw
			edits[current].field = node.Raw
		default:
			return nil, fmt.Errorf("unsupported selection expression")
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	if len(edits) != len(manifest.Product) || index < 0 || index > len(edits) {
		return nil, fmt.Errorf("unsupported product selection shape")
	}
	if index == len(edits) {
		if source == "" {
			return nil, fmt.Errorf("no selection at index %d", index)
		}
		return append(bytes.Clone(data), []byte("\n[[product]]\nsource = "+strconv.Quote(source)+"\n")...), nil
	}
	edit := edits[index]
	if edit.source.Length == 0 {
		return nil, fmt.Errorf("product selection has no source string")
	}
	if source != "" {
		out := append(bytes.Clone(data[:edit.source.Offset]), []byte(strconv.Quote(source))...)
		return append(out, data[edit.source.Offset+edit.source.Length:]...), nil
	}
	if edit.extra {
		return nil, fmt.Errorf("cannot remove a product with extension fields; edit its table explicitly")
	}
	out := bytes.Clone(data[:edit.header.Offset])
	out = append(out, data[edit.header.Offset+edit.header.Length:edit.field.Offset]...)
	return append(out, data[edit.field.Offset+edit.field.Length:]...), nil
}
