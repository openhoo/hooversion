// Package tomledit validates TOML and edits only selected string tokens.
// The dependency's unstable AST is isolated here and pinned in go.mod.
package tomledit

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

type String struct {
	Path       []string
	Value      string
	Start, End int
}
type edit struct {
	start, end int
	value      string
}
type Document struct {
	Data    []byte
	Values  map[string]any
	Strings []String
	edits   map[int]edit
}

func Parse(data []byte) (*Document, error) {
	d := &Document{Data: data, edits: map[int]edit{}}
	if err := toml.Unmarshal(data, &d.Values); err != nil {
		return nil, err
	}
	var p unstable.Parser
	p.Reset(data)
	var table []string
	counts := map[string]int{}
	arrayTables := map[string]int{}
	keys := func(n *unstable.Node) []string {
		var out []string
		it := n.Key()
		for it.Next() {
			out = append(out, string(it.Node().Data))
		}
		return out
	}
	var walk func(*unstable.Node, []string)
	walk = func(n *unstable.Node, path []string) {
		switch n.Kind {
		case unstable.String:
			d.Strings = append(d.Strings, String{append([]string(nil), path...), string(n.Data), int(n.Raw.Offset), int(n.Raw.Offset + n.Raw.Length)})
		case unstable.Array:
			it := n.Children()
			i := 0
			for it.Next() {
				walk(it.Node(), append(append([]string(nil), path...), strconv.Itoa(i)))
				i++
			}
		case unstable.InlineTable:
			it := n.Children()
			for it.Next() {
				kv := it.Node()
				walk(kv.Value(), append(append([]string(nil), path...), keys(kv)...))
			}
		}
	}
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			raw := keys(n)
			table = nil
			for i := 0; i < len(raw); i++ {
				table = append(table, raw[i])
				prefix := strings.Join(table, "\x00")
				if prior, ok := arrayTables[prefix]; ok && !(n.Kind == unstable.ArrayTable && i == len(raw)-1) {
					table = append(table, strconv.Itoa(prior))
				}
			}
			if n.Kind == unstable.ArrayTable {
				k := strings.Join(table, "\x00")
				index := counts[k]
				counts[k]++
				table = append(table, strconv.Itoa(index))
				arrayTables[k] = index
			}
		case unstable.KeyValue:
			walk(n.Value(), append(append([]string(nil), table...), keys(n)...))
		}
	}
	if err := p.Error(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Document) Get(path ...string) any {
	var value any = d.Values
	for _, key := range path {
		switch node := value.(type) {
		case map[string]any:
			value = node[key]
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(node) {
				return nil
			}
			value = node[i]
		default:
			return nil
		}
	}
	return value
}
func (d *Document) Text(path ...string) (string, bool) {
	v, ok := d.Get(path...).(string)
	return v, ok
}
func Equal(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }
func (d *Document) Set(path []string, value string) error {
	for _, s := range d.Strings {
		if Equal(s.Path, path) {
			return d.SetString(s, value)
		}
	}
	return fmt.Errorf("TOML %s has no editable string", strings.Join(path, "."))
}
func (d *Document) SetString(s String, value string) error {
	if s.Start < 0 || s.End > slen(d.Data) || s.Start >= s.End {
		return fmt.Errorf("invalid TOML string range")
	}
	if s.Value == value {
		return nil
	}
	raw := string(d.Data[s.Start:s.End])
	encoded := strconv.Quote(value)
	// Retain literal quoting when the new value can be represented in it.
	if strings.HasPrefix(raw, "'") && !strings.ContainsAny(value, "'\r\n") {
		encoded = "'" + value + "'"
	}
	if existing, ok := d.edits[s.Start]; ok && existing.value != encoded {
		return fmt.Errorf("conflicting TOML edits")
	}
	d.edits[s.Start] = edit{s.Start, s.End, encoded}
	return nil
}
func slen(b []byte) int { return len(b) }
func (d *Document) Render() ([]byte, error) {
	edits := make([]edit, 0, len(d.edits))
	for _, e := range d.edits {
		edits = append(edits, e)
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out []byte
	pos := 0
	for _, e := range edits {
		if e.start < pos {
			return nil, fmt.Errorf("overlapping TOML edits")
		}
		out = append(out, d.Data[pos:e.start]...)
		out = append(out, e.value...)
		pos = e.end
	}
	out = append(out, d.Data[pos:]...)
	var check map[string]any
	if err := toml.Unmarshal(out, &check); err != nil {
		return nil, err
	}
	return out, nil
}
