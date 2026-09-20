package pipeline

import (
	"bytes"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// FieldsFile is the catalogue of what a caller can tell ideacheck about an idea.
const FieldsFile = "fields.yaml"

// Field is one intake field in the words a caller needs to fill it in.
type Field struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Example     string `yaml:"example" json:"example"`
	// Flag is how this field is passed on the command line; it is derived, not
	// stored, so the catalogue never drifts from the flag surface.
	Flag string `yaml:"-" json:"flag"`
}

// Fields is the catalogue: the idea fields, then the ones about the person.
type Fields struct {
	Idea    []Field `yaml:"idea" json:"idea"`
	Profile []Field `yaml:"profile" json:"profile"`
}

// LoadFields reads fields.yaml and checks it against the intake: every field the
// intake accepts is described exactly once, and nothing else is.
func LoadFields(r interface{ Read(string) ([]byte, error) }) (*Fields, error) {
	b, err := r.Read(FieldsFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FieldsFile, err)
	}
	var f Fields
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", FieldsFile, err)
	}
	if err := describes(f.Idea, ideaFields, ""); err != nil {
		return nil, fmt.Errorf("%s idea: %w", FieldsFile, err)
	}
	if err := describes(f.Profile, profileFields, profilePrefix); err != nil {
		return nil, fmt.Errorf("%s profile: %w", FieldsFile, err)
	}
	for i := range f.Idea {
		f.Idea[i].Flag = "--answer " + f.Idea[i].Name + "="
	}
	for i := range f.Profile {
		f.Profile[i].Flag = "--answer " + profilePrefix + f.Profile[i].Name + "="
	}
	return &f, nil
}

func describes(described []Field, want []string, prefix string) error {
	got := make([]string, 0, len(described))
	for _, f := range described {
		if f.Description == "" {
			return fmt.Errorf("%s: missing description", f.Name)
		}
		got = append(got, f.Name)
	}
	missing, extra := diff(want, got), diff(got, want)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("describes %v and leaves %v undescribed (the intake takes %s%v)", extra, missing, prefix, want)
	}
	return nil
}

// diff is the names in a that b does not have.
func diff(a, b []string) []string {
	var out []string
	for _, name := range a {
		if !contains(b, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
