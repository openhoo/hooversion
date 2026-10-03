package tomledit

import (
	"strings"
	"testing"
)

func TestEditPreservesRealTOMLSyntax(t *testing.T) {
	data := []byte("# heading\r\n[\"project\"] # comment\r\n\"name\" = 'my_app'\r\nversion = \"\"\"1.0.0\"\"\" # version\r\ndependencies = [\r\n  'local[extra]>=1; python_version >= \"3.10\"', # retain\r\n]\r\n[tool.poetry.dependencies]\r\nlocal = {version = '1.0.0', path = '../local'}\r\n")
	d, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Set([]string{"project", "version"}, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := d.Set([]string{"tool", "poetry", "dependencies", "local", "version"}, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	out, err := d.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(string(data), "\"\"\"1.0.0\"\"\"", "\"2.0.0\"", 1)
	want = strings.Replace(want, "version = '1.0.0'", "version = '2.0.0'", 1)
	if string(out) != want {
		t.Fatalf("unexpected edit:\n%s", out)
	}
}
func TestArrayTableSpansAndNestedTables(t *testing.T) {
	d, err := Parse([]byte("[[package]]\nname = 'one'\nversion = '1'\n[[package]]\nname = 'two'\nversion = '1'\n[package.metadata]\nlabel = 'keep'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Set([]string{"package", "1", "version"}, "2"); err != nil {
		t.Fatal(err)
	}
	if err := d.Set([]string{"package", "1", "metadata", "label"}, "changed"); err != nil {
		t.Fatal(err)
	}
	out, err := d.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "version = '1'") != 1 || !strings.Contains(string(out), "label = 'changed'") {
		t.Fatal(string(out))
	}
}
func TestInvalidAndConflictingEditsRejected(t *testing.T) {
	if _, err := Parse([]byte("x=1\nx=2\n")); err == nil {
		t.Fatal("accepted duplicate key")
	}
	d, err := Parse([]byte("x='one'"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Set([]string{"x"}, "two"); err != nil {
		t.Fatal(err)
	}
	if err := d.Set([]string{"x"}, "three"); err == nil {
		t.Fatal("accepted conflicting edit")
	}
}

func TestNestedArrayTableCountersAreScopedToParent(t *testing.T) {
	d, err := Parse([]byte("[[fruits]]\nname='apple'\n[[fruits.varieties]]\nname='red'\n[[fruits]]\nname='pear'\n[[fruits.varieties]]\nname='green'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Set([]string{"fruits", "1", "varieties", "0", "name"}, "yellow"); err != nil {
		t.Fatal(err)
	}
	out, err := d.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "name='red'") || !strings.Contains(string(out), "name='yellow'") {
		t.Fatal(string(out))
	}
}
