package msg

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// updating reports whether JF_UPDATE_GOLDEN=1 asks to rewrite goldens.
func updating() bool { return os.Getenv("JF_UPDATE_GOLDEN") == "1" }

const kindsGolden = "testdata/kinds.golden"

// shape describes the JSON a type encodes to: field keys and their shapes,
// recursively. Renaming, retyping, adding or removing a field changes it. A
// nested Part stays a name, so a new Kind's field never reshapes another
// Kind.
func shape(t reflect.Type, seen map[reflect.Type]bool) string {
	if t == reflect.TypeFor[Part]() {
		return "Part"
	}
	// A named type can marshal its own way (json.RawMessage vs []byte).
	if t.Name() != "" && t.Kind() != reflect.Struct {
		return t.String()
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + shape(t.Elem(), seen)
	case reflect.Slice:
		return "[]" + shape(t.Elem(), seen)
	case reflect.Struct:
		if seen[t] {
			return t.Name()
		}
		seen[t] = true
		defer delete(seen, t)
		var fs []string
		for i := range t.NumField() {
			f := t.Field(i)
			if f.Tag.Get("json") == "-" {
				continue
			}
			fs = append(fs, f.Tag.Get("json")+":"+shape(f.Type, seen))
		}
		return "{" + strings.Join(fs, ",") + "}"
	}
	return t.String()
}

// kindShapes is each Kind's shape: the Part fields it sets.
func kindShapes() map[Kind]string {
	part := reflect.TypeFor[Part]()
	out := map[Kind]string{}
	for k, fields := range kindFields() {
		var fs []string
		for _, name := range fields {
			f, _ := part.FieldByName(name)
			fs = append(fs, f.Tag.Get("json")+":"+shape(f.Type, map[reflect.Type]bool{}))
		}
		out[k] = strings.Join(fs, ",")
	}
	return out
}

func readGolden(t *testing.T) (int, map[Kind]string) {
	t.Helper()
	b, err := os.ReadFile(kindsGolden)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	v, err := strconv.Atoi(strings.TrimPrefix(lines[0], "msg_v "))
	if err != nil {
		t.Fatalf("golden line 1 %q: want msg_v N", lines[0])
	}
	kinds := map[Kind]string{}
	for _, l := range lines[1:] {
		k, s, _ := strings.Cut(l, " ")
		kinds[Kind(k)] = s
	}
	return v, kinds
}

func writeGolden(t *testing.T, kinds map[Kind]string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "msg_v %d\n", CurrentVersion)
	for _, k := range slices.Sorted(maps.Keys(kinds)) {
		fmt.Fprintf(&b, "%s %s\n", k, kinds[k])
	}
	if err := os.WriteFile(kindsGolden, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestKindShapeChangeBumpsMsgV enforces ADR 0002's rule: a new Kind is
// additive and keeps msg_v, a changed shape of an existing Kind needs a new
// msg_v and an upcaster. Run with JF_UPDATE_GOLDEN=1 to record an allowed change.
func TestKindShapeChangeBumpsMsgV(t *testing.T) {
	goldenV, golden := readGolden(t)
	current := kindShapes()

	var changed, added []Kind
	for k, s := range current {
		old, ok := golden[k]
		switch {
		case !ok:
			added = append(added, k)
		case old != s:
			changed = append(changed, k)
		}
	}
	if len(changed) > 0 && CurrentVersion == goldenV {
		t.Fatalf("Kinds %v changed shape: bump CurrentVersion and add an upcaster", changed)
	}
	if updating() {
		writeGolden(t, current)
		return
	}
	if CurrentVersion != goldenV || len(changed) > 0 || len(added) > 0 {
		t.Fatalf("golden is stale (msg_v %d, changed %v, added %v): rerun with JF_UPDATE_GOLDEN=1", goldenV, changed, added)
	}
}

func TestShapeSeesFieldChanges(t *testing.T) {
	type a struct {
		X string `json:"x"`
	}
	type renamed struct {
		X string `json:"y"`
	}
	type retyped struct {
		X int `json:"x"`
	}
	type grown struct {
		X string `json:"x"`
		Y string `json:"y,omitempty"`
	}
	base := shape(reflect.TypeFor[a](), map[reflect.Type]bool{})
	for _, other := range []reflect.Type{reflect.TypeFor[renamed](), reflect.TypeFor[retyped](), reflect.TypeFor[grown]()} {
		if shape(other, map[reflect.Type]bool{}) == base {
			t.Errorf("%v has the same shape as %v", other, reflect.TypeFor[a]())
		}
	}

	// Raw JSON and bytes share an underlying type but encode differently.
	type raw struct {
		X json.RawMessage `json:"x"`
	}
	type bytes struct {
		X []byte `json:"x"`
	}
	if shape(reflect.TypeFor[raw](), map[reflect.Type]bool{}) == shape(reflect.TypeFor[bytes](), map[reflect.Type]bool{}) {
		t.Error("json.RawMessage and []byte have the same shape")
	}
}
