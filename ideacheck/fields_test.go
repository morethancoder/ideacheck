package ideacheck

import (
	"testing"

	"github.com/morethancoder/ideacheck/internal/config"
)

// The catalogue is what a caller reads to know what to send, so it must cover
// exactly what the intake accepts — no undescribed field, no invented one.
func TestFieldsCatalogueCoversTheIntake(t *testing.T) {
	f, err := LoadFields(config.NewFiles(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Idea) != len(IdeaFields()) || len(f.Profile) != len(ProfileFields()) {
		t.Fatalf("catalogue has %d idea and %d profile fields, intake takes %d and %d",
			len(f.Idea), len(f.Profile), len(IdeaFields()), len(ProfileFields()))
	}
	for _, field := range f.Idea {
		if !KnownField(field.Name) || field.Flag != "--answer "+field.Name+"=" {
			t.Errorf("idea field %+v", field)
		}
	}
	for _, field := range f.Profile {
		if !KnownField(profilePrefix+field.Name) || field.Flag != "--answer profile."+field.Name+"=" {
			t.Errorf("profile field %+v", field)
		}
	}
}

func TestFieldsCatalogueRejectsDrift(t *testing.T) {
	extra := memFiles{FieldsFile: "idea:\n  - name: nope\n    description: x\n"}
	if _, err := LoadFields(extra); err == nil || !containsSub(err.Error(), "nope") {
		t.Errorf("a field the intake does not take must be rejected: %v", err)
	}
}
